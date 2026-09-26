package queue

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// Task type names shared by the enqueuer and the worker. Keeping them constant
// in one place avoids typos drifting between the client and server side.
const (
	// TaskTypeIdempotencyPurge deletes idempotency-key rows older than the
	// retention window (24h). Scheduled hourly via RegisterPeriodic — the
	// middleware claims keys for 30 min (request_in_progress), keeps the
	// response for 24h, and this job sweeps the rest.
	TaskTypeIdempotencyPurge = "idempotency:purge"
)

// ClientOpts converts a redis:// URL into the options both the enqueuer
// (asynq.Client) and the worker (asynq.Server) need. Reusing the same Redis
// that already serves the cart Reserver keeps the infrastructure to one
// process — the VPS runs exactly one Redis (shopkeet-redis), and Asynq stores
// its queues in a Redis hash namespace, so reservations (cache) and job queues
// (Asynq) coexist without touching each other's keys.
func ClientOpts(redisURL string) (asynq.RedisClientOpt, error) {
	if redisURL == "" {
		return asynq.RedisClientOpt{}, fmt.Errorf("redis url is empty")
	}
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return asynq.RedisClientOpt{}, fmt.Errorf("parse redis url: %w", err)
	}
	return asynq.RedisClientOpt{
		Addr: opt.Addr,
		DB:   opt.DB,
	}, nil
}

// Enqueuer wraps an asynq.Client for enqueueing jobs. Created once at startup.
type Enqueuer struct {
	client *asynq.Client
}

// NewEnqueuer builds the enqueue side. Jobs are enqueued synchronously at
// request time (order confirmation, shipment update, webhook retry, ...).
func NewEnqueuer(redisURL string) (*Enqueuer, error) {
	o, err := ClientOpts(redisURL)
	if err != nil {
		return nil, err
	}
	return &Enqueuer{client: asynq.NewClient(o)}, nil
}

// Enqueue schedules a task immediately. Returns an error only if the queue is
// unreachable — callers may treat it as best-effort (never fail the request
// that produced the job, same rule the notification providers follow).
func (e *Enqueuer) Enqueue(ctx context.Context, task *asynq.Task) error {
	if e == nil || e.client == nil || task == nil {
		return nil
	}
	if _, err := e.client.Enqueue(task, asynq.MaxRetry(3)); err != nil {
		return fmt.Errorf("enqueue %s: %w", task.Type(), err)
	}
	return nil
}

// EnqueueIn schedules a task after a delay (retry/reminder jobs).
func (e *Enqueuer) EnqueueIn(ctx context.Context, task *asynq.Task, delay time.Duration) error {
	if e == nil || e.client == nil || task == nil {
		return nil
	}
	if _, err := e.client.Enqueue(task, asynq.MaxRetry(3), asynq.ProcessIn(delay)); err != nil {
		return fmt.Errorf("enqueue %s: %w", task.Type(), err)
	}
	return nil
}

// Close releases the enqueuer's pooled connections.
func (e *Enqueuer) Close() error {
	if e == nil || e.client == nil {
		return nil
	}
	return e.client.Close()
}

// Worker is the consumer side: an asynq.Server with a mux of registered task
// handlers plus an optional periodic scheduler. It is run in its own goroutine
// so the HTTP API never blocks on job processing.
type Worker struct {
	server    *asynq.Server
	scheduler *asynq.Scheduler
	mux       *asynq.ServeMux
}

// NewWorker builds the worker (server + mux). Register handlers with Register,
// then Start. When a periodic spec is provided the scheduler is also started.
func NewWorker(redisURL string, concurrency int) (*Worker, error) {
	o, err := ClientOpts(redisURL)
	if err != nil {
		return nil, err
	}
	w := &Worker{
		server: asynq.NewServer(o, asynq.Config{
			Concurrency: concurrency,
			Queues:      map[string]int{"default": 6, "critical": 3, "low": 1},
		}),
		scheduler: asynq.NewScheduler(o, nil),
		mux:       asynq.NewServeMux(),
	}
	return w, nil
}

// Register wires a handler for a task type.
func (w *Worker) Register(taskType string, h asynq.HandlerFunc) {
	w.mux.HandleFunc(taskType, h)
}

// RegisterPeriodic schedules a task on a cron spec (6-field robfig/cron, e.g.
// "@every 1h"). The scheduler enqueues the task; the worker executes it.
func (w *Worker) RegisterPeriodic(spec string, task *asynq.Task) error {
	if w == nil || w.scheduler == nil {
		return nil
	}
	if _, err := w.scheduler.Register(spec, task); err != nil {
		return fmt.Errorf("register periodic %s: %w", spec, err)
	}
	return nil
}

// Start launches the worker goroutine(s). Establishes the scheduler if any
// periodic tasks were registered. Does not block. Errors surface via log.Fatal —
// a misconfigured worker should fail startup loudly, not run half-wired.
func (w *Worker) Start() {
	if w.scheduler != nil {
		if err := w.scheduler.Start(); err != nil {
			log.Fatalf("queue: periodic scheduler failed to start: %v", err)
		}
	}
	srv := w.server
	mux := w.mux
	go func() {
		if err := srv.Run(mux); err != nil {
			log.Printf("queue: asynq server stopped: %v", err)
		}
	}()
}

// Stop gracefully drains the worker.
func (w *Worker) Stop() {
	if w.scheduler != nil {
		w.scheduler.Shutdown()
	}
	if w.server != nil {
		w.server.Shutdown()
	}
}