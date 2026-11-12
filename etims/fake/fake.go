// Package fake is an in-memory route for application tests: it validates
// like a real route, signs deterministically, remembers what it was sent,
// and can be told to fail in each of the ways the contract allows.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"sync"
	"time"

	"github.com/kennankole/drop-eTIMS-sdk/etims"
)

// Route is a fake etims.Invoicer, etims.ItemRegistrar and etims.Catalog.
// Safe for concurrent use.
type Route struct {
	mu sync.Mutex

	// Fail, when set, is returned by every call until cleared. Set it to an
	// *etims.ResultError or one of the sentinels to simulate KRA.
	Fail error
	// Now supplies the signing time; defaults to time.Now.
	Now func() time.Time

	invoices map[int64]etims.Signature
	sent     []etims.Invoice
	items    map[string]etims.Item
	classes  []etims.ItemClass
	nextRcpt int64
}

// New returns an empty route with a few item classes preloaded.
func New() *Route {
	return &Route{
		Now:      time.Now,
		invoices: map[int64]etims.Signature{},
		items:    map[string]etims.Item{},
		classes: []etims.ItemClass{
			{Code: "5059690800", Name: "Services", Level: 5, TaxType: etims.TaxStandard, Active: true},
			{Code: "5022110801", Name: "Food", Level: 5, TaxType: etims.TaxStandard, Active: true},
		},
	}
}

// SubmitInvoice validates, refuses a reused invoice number with
// ErrDuplicate, and otherwise signs the invoice deterministically from its
// number and lines.
func (r *Route) SubmitInvoice(_ context.Context, inv *etims.Invoice) (etims.Signature, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Fail != nil {
		return etims.Signature{}, r.Fail
	}

	if err := inv.Validate(); err != nil {
		return etims.Signature{}, err
	}

	if _, dup := r.invoices[inv.InvoiceNo]; dup {
		return etims.Signature{}, &etims.ResultError{Code: "994", Message: "There is an overlapped Data", Kind: etims.ErrDuplicate}
	}

	r.nextRcpt++

	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%d", inv.InvoiceNo, inv.TraderInvoiceNo, inv.Totals().TotalCents)))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:])

	sig := etims.Signature{
		ReceiptNo:      r.nextRcpt,
		TotalReceiptNo: r.nextRcpt,
		InternalData:   enc[:26],
		ReceiptSign:    enc[26:42],
		SignedAt:       r.Now(),
	}

	r.invoices[inv.InvoiceNo] = sig
	r.sent = append(r.sent, *inv)

	return sig, nil
}

// RegisterItem stores the item by code.
func (r *Route) RegisterItem(_ context.Context, item *etims.Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Fail != nil {
		return r.Fail
	}

	if item.Code == "" {
		return fmt.Errorf("%w: item code is required", etims.ErrInvalidInvoice)
	}

	r.items[item.Code] = *item

	return nil
}

// ItemClasses returns the preloaded classes.
func (r *Route) ItemClasses(context.Context, time.Time) ([]etims.ItemClass, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Fail != nil {
		return nil, r.Fail
	}

	return append([]etims.ItemClass(nil), r.classes...), nil
}

// Codes returns the five tax types.
func (r *Route) Codes(context.Context, time.Time) ([]etims.Code, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Fail != nil {
		return nil, r.Fail
	}

	out := make([]etims.Code, 0, len(etims.AllTaxTypes))
	for i, t := range etims.AllTaxTypes {
		out = append(out, etims.Code{Class: "04", ClassName: "Taxation Type", Code: string(t), Name: string(t), SortOrder: i + 1,
			UserDefined1: fmt.Sprint(t.RateBps() / 100), Active: true})
	}

	return out, nil
}

// Branches returns one head office.
func (r *Route) Branches(context.Context, time.Time) ([]etims.BranchInfo, error) {
	if r.Fail != nil {
		return nil, r.Fail
	}

	return []etims.BranchInfo{{Branch: etims.Branch{PIN: "P000000000A", BranchID: "00"}, Name: "Headquarters", StatusCode: "01", HeadOffice: true}}, nil
}

// Sent returns every invoice signed so far, in order.
func (r *Route) Sent() []etims.Invoice {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]etims.Invoice(nil), r.sent...)
}

// Signature returns the signature issued for an invoice number.
func (r *Route) Signature(invoiceNo int64) (etims.Signature, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.invoices[invoiceNo]

	return s, ok
}

// Items returns the registered items by code.
func (r *Route) Items() map[string]etims.Item {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[string]etims.Item, len(r.items))
	for k := range r.items {
		out[k] = r.items[k]
	}

	return out
}

var (
	_ etims.Invoicer      = (*Route)(nil)
	_ etims.ItemRegistrar = (*Route)(nil)
	_ etims.Catalog       = (*Route)(nil)
)
