-- Phase 12: Order & Account Notifications.
-- Delivery log for customer communication (order confirmation, shipment
-- updates, welcome). Rows are written from the notification event subscribers;
-- status is 'sent' when the provider accepted the send, 'failed' when it didn't
-- (a failed send never fails the checkout that produced the event). RLS +
-- FORCE + OWNER keep tenants from seeing each other's delivery history. Pure
-- DDL, so this runs as shopkeet_app like 0011/0012/0013.

CREATE TABLE notification_log (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  notification_type TEXT NOT NULL, -- 'order_confirmation', 'order_shipped', 'order_delivered', 'customer_welcome'
  recipient TEXT NOT NULL,         -- email or phone
  order_id UUID REFERENCES orders(id),
  status TEXT NOT NULL DEFAULT 'sent', -- sent, failed
  sent_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE notification_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_log FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_log
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE notification_log OWNER TO shopkeet_app;
CREATE INDEX notification_log_tenant_sent_idx ON notification_log (tenant_id, sent_at);