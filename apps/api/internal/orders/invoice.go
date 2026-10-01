package orders

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// invoiceLine is one row of the PDF's line-items table, enriched with the
// product name and variant SKU so the invoice reads like a real document
// (orders carry variant_id; names live on products/variants).
type invoiceLine struct {
	name string
	sku  string
	qty  int
	unit int
	line int
}

// invoiceTotals mirrors the order's money columns exactly. The total MUST equal
// orders.total_cents — the acceptance criterion — by construction: subtotal is
// the sum of line items, and total = subtotal + shipping + tax − discount − gift.
type invoiceTotals struct {
	subtotal int
	shipping int
	discount int
	gift     int
	tax      int
	total    int
}

func computeInvoice(o *orderRow, lines []invoiceLine) invoiceTotals {
	st := 0
	for _, l := range lines {
		st += l.line
	}
	return invoiceTotals{
		subtotal: st,
		shipping: o.shippingCostCents,
		discount: o.discountCents,
		gift:     o.giftCardCents,
		tax:      o.taxCents,
		total:    st + o.shippingCostCents - o.discountCents - o.giftCardCents + o.taxCents,
	}
}

func loadInvoiceLines(c *fiber.Ctx, tx pgx.Tx, orderID string) ([]invoiceLine, error) {
	rows, err := tx.Query(c.Context(), `
		SELECT COALESCE(p.name, ''), COALESCE(v.sku, ''), oi.quantity, oi.unit_price_cents
		FROM order_items oi
		LEFT JOIN product_variants v ON v.id = oi.variant_id
		LEFT JOIN products p ON p.id = oi.product_id
		WHERE oi.order_id = $1
		ORDER BY oi.id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]invoiceLine, 0)
	for rows.Next() {
		var l invoiceLine
		if err := rows.Scan(&l.name, &l.sku, &l.qty, &l.unit); err != nil {
			return nil, err
		}
		l.line = l.qty * l.unit
		list = append(list, l)
	}
	return list, rows.Err()
}

// money formats a cent value with a currency symbol so the PDF's numbers read
// like a merchant's invoice rather than raw machine integers.
func money(currency string, cents int) string {
	sym := "$"
	long := ""
	switch strings.ToLower(currency) {
	case "eur":
		sym = "€"
	case "gbp":
		sym = "£"
	case "pkr":
		sym = ""
		long = "Rs "
	case "usd", "":
	default:
		sym = strings.ToUpper(currency) + " "
	}
	return long + sym + fmt.Sprintf("%.2f", float64(cents)/100)
}

// lat1 maps a UTF-8 string down to Latin-1 so fpdf's core (non-TTF) fonts can
// render arbitrary product names without erroring; out-of-range runes become '?'.
func lat1(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 256 {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return b.String()
}

// renderInvoice draws a single A4 invoice and returns the PDF bytes. Column
// layout: QTY | ITEM (name + SKU) | UNIT | LINE TOTAL, then a money block for
// shipping/discount/gift-card/tax and a bold TOTAL that equals o.total_cents.
func renderInvoice(o *orderRow, lines []invoiceLine, storeName string) ([]byte, error) {
	t := computeInvoice(o, lines)
	cur := o.currency

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 20, 20)
	pdf.SetAutoPageBreak(true, 25)
	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 18)
	pdf.Cell(0, 10, "INVOICE")
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(80, 80, 80)
	pdf.Cell(0, 5, lat1(storeName))
	pdf.Ln(5)
	pdf.Cell(0, 5, "Order "+o.id)
	pdf.Ln(5)
	pdf.Cell(0, 5, "Date "+o.createdAt.Format("2006-01-02"))
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(8)

	// Bill to / ship to block.
	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Customer")
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "", 9)
	addr := []string{o.customerName, o.customerPhone, strp(o.customerEmail)}
	for _, a := range addr {
		if a != "" {
			pdf.Cell(0, 5, lat1(a))
			pdf.Ln(5)
		}
	}
	if line := shippingAddressLine(o); line != "" {
		for _, part := range strings.Split(line, "\n") {
			pdf.Cell(0, 5, lat1(part))
			pdf.Ln(5)
		}
	}
	pdf.Ln(6)

	// Line items table.
	pdf.SetFont("Helvetica", "B", 9)
	colQty := 20.0
	colItem := 95.0
	colUnit := 30.0
	colLine := 35.0
	pdf.SetFillColor(230, 230, 230)
	pdf.CellFormat(colQty, 7, "QTY", "1", 0, "C", true, 0, "")
	pdf.CellFormat(colItem, 7, "ITEM", "1", 0, "L", true, 0, "")
	pdf.CellFormat(colUnit, 7, "UNIT", "1", 0, "R", true, 0, "")
	pdf.CellFormat(colLine, 7, "LINE", "1", 0, "R", true, 0, "")
	pdf.Ln(-1)

	pdf.SetFont("Helvetica", "", 9)
	for _, l := range lines {
		name := lat1(l.name)
		if l.sku != "" {
			name += "  (" + lat1(l.sku) + ")"
		}
		pdf.CellFormat(colQty, 7, fmt.Sprintf("%d", l.qty), "1", 0, "C", false, 0, "")
		pdf.CellFormat(colItem, 7, name, "1", 0, "L", false, 0, "")
		pdf.CellFormat(colUnit, 7, money(cur, l.unit), "1", 0, "R", false, 0, "")
		pdf.CellFormat(colLine, 7, money(cur, l.line), "1", 0, "R", false, 0, "")
		pdf.Ln(-1)
	}

	// Money block.
	right := 20.0 + colQty + colItem + colUnit
	row := func(label string, val string, bold bool) {
		if bold {
			pdf.SetFont("Helvetica", "B", 9)
		} else {
			pdf.SetFont("Helvetica", "", 9)
		}
		pdf.SetX(right)
		pdf.CellFormat(colLine, 6, label, "", 0, "L", false, 0, "")
		pdf.CellFormat(35, 6, val, "", 0, "R", false, 0, "")
		pdf.Ln(-1)
	}
	row("Subtotal", money(cur, t.subtotal), false)
	if t.shipping > 0 {
		row("Shipping", money(cur, t.shipping), false)
	}
	if t.discount > 0 {
		row("Discount", "-"+money(cur, t.discount), false)
	}
	if t.gift > 0 {
		row("Gift card", "-"+money(cur, t.gift), false)
	}
	if t.tax > 0 {
		row("Tax", money(cur, t.tax), false)
	}
	row("TOTAL", money(cur, t.total), true)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func shippingAddressLine(o *orderRow) string {
	var parts []string
	if s := strp(o.line1); s != "" {
		parts = append(parts, s)
	}
	if s := strp(o.line2); s != "" {
		parts = append(parts, s)
	}
	var city []string
	if s := strp(o.city); s != "" {
		city = append(city, s)
	}
	if s := strp(o.state); s != "" {
		city = append(city, s)
	}
	if s := strp(o.postalCode); s != "" {
		city = append(city, s)
	}
	if len(city) > 0 {
		parts = append(parts, strings.Join(city, ", "))
	}
	if s := strp(o.country); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

// GetInvoice handles GET /orders/:id/invoice.pdf (Admin via merchant JWT, or
// Customer via the same phone[/email] lookup as GET /orders/:id — the
// MerchantOrCustomerMW route). Generates a line-itemized PDF server-side; the
// TOTAL line equals orders.total_cents by construction.
func (s *Service) GetInvoice(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	orderID := c.Params("id")

	var order *orderRow
	var err error
	if actor, _ := c.Locals("actor").(string); actor == "merchant" {
		order, err = loadOrder(c, tx, "id = $1", orderID)
	} else {
		phone := c.Query("phone")
		if phone == "" {
			return httperr.C(fiber.StatusBadRequest, "phone query param required")
		}
		if email := c.Query("email"); email != "" {
			order, err = loadOrder(c, tx,
				"id = $1 AND customer_phone = $2 AND customer_email = $3",
				orderID, phone, email)
		} else {
			order, err = loadOrder(c, tx, "id = $1 AND customer_phone = $2",
				orderID, phone)
		}
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if order == nil {
		return httperr.C(fiber.StatusNotFound, "order not found")
	}

	lines, err := loadInvoiceLines(c, tx, order.id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	storeName := "Shopkeet"
	if tid, _ := c.Locals("tenant_id").(string); tid != "" {
		var name *string
		if err := tx.QueryRow(c.Context(),
			"SELECT name FROM tenants WHERE id = $1", tid).Scan(&name); err == nil && name != nil {
			if *name != "" {
				storeName = *name
			}
		}
	}
	pdfBytes, err := renderInvoice(order, lines, storeName)
	if err != nil {
		return httperr.ErrInternalServerError
	}

	c.Set("Content-Type", "application/pdf")
	c.Set("Content-Disposition", `attachment; filename="invoice-`+order.id+`.pdf"`)
	return c.Send(pdfBytes)
}
