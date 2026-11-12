// Package etims defines a vendor-neutral vocabulary and ports for issuing
// electronic tax invoices through KRA's eTIMS (Kenya): what is sold (Item,
// Line), to whom (Party), from which registered branch (Branch), and what
// KRA hands back when it signs the invoice (Signature).
//
// Application code should depend only on the types in this package, never on
// a route package (etims/oscu, a certified integrator, etims/fake) outside of
// the composition root that constructs it. That is what lets the route to KRA
// change without touching business logic.
//
// Money is in cents (int64) and quantities in hundredths (Qty); the wire
// format KRA wants, decimal numbers with two places, is each route package's
// concern. Prices are VAT-inclusive, as they are on Kenyan shelves and on
// KRA's own samples: a line's taxable amount is its gross, and the tax is the
// part of that gross the rate accounts for.
package etims

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// TaxType is KRA's taxation type code (OSCU specification §4.1), the "A to E"
// rows printed on every Kenyan tax invoice.
type TaxType string

const (
	// TaxExempt (A): the customer pays no VAT and the seller cannot reclaim
	// input VAT on what it bought to make the sale.
	TaxExempt TaxType = "A"
	// TaxStandard (B): 16%.
	TaxStandard TaxType = "B"
	// TaxZeroRated (C): the customer pays no VAT, the seller can reclaim
	// input VAT. Not the same as A, which is why the code is kept, not "0%".
	TaxZeroRated TaxType = "C"
	// TaxNonVAT (D): the seller is not VAT-registered; every line is D.
	TaxNonVAT TaxType = "D"
	// TaxReduced (E): 8%, fuel.
	TaxReduced TaxType = "E"
)

// AllTaxTypes in the order KRA lists them, which is the order of the per-type
// totals on an invoice header.
var AllTaxTypes = []TaxType{TaxExempt, TaxStandard, TaxZeroRated, TaxNonVAT, TaxReduced}

// RateBps is the rate in basis points (16% = 1600). Rates are KRA code-list
// data, not law baked into the SDK; override them through Rates if KRA's
// list changes.
func (t TaxType) RateBps() int64 {
	return DefaultRates[t]
}

// Valid reports whether t is one of A to E.
func (t TaxType) Valid() bool {
	_, ok := DefaultRates[t]

	return ok
}

// DefaultRates are the rates the OSCU specification's code list carries for
// each tax type.
var DefaultRates = map[TaxType]int64{
	TaxExempt:    0,
	TaxStandard:  1_600,
	TaxZeroRated: 0,
	TaxNonVAT:    0,
	TaxReduced:   800,
}

// ProductType is the item type code (§4.3).
type ProductType string

const (
	ProductRawMaterial ProductType = "1"
	ProductFinished    ProductType = "2"
	// ProductService is a service without stock: a delivery fee, a platform
	// commission, a service fee.
	ProductService ProductType = "3"
)

// ReceiptType is the sales receipt type (§4.10).
type ReceiptType string

const (
	// ReceiptSale is a normal sale.
	ReceiptSale ReceiptType = "S"
	// ReceiptCreditNote is a credit note after a sale; the invoice must name
	// the original invoice number. KRA has no cancel operation: a correction
	// is a credit note.
	ReceiptCreditNote ReceiptType = "R"
)

// TransactionType is the sales type (§4.9). Only Normal is used for real
// sales; the others exist for completeness.
type TransactionType string

const (
	TransactionNormal   TransactionType = "N"
	TransactionCopy     TransactionType = "C"
	TransactionProforma TransactionType = "P"
	TransactionTraining TransactionType = "T"
)

// PaymentMethod is the payment type code (§4.11).
type PaymentMethod string

const (
	PaymentCash        PaymentMethod = "01"
	PaymentCredit      PaymentMethod = "02"
	PaymentCashCredit  PaymentMethod = "03"
	PaymentBankCheque  PaymentMethod = "04"
	PaymentCard        PaymentMethod = "05"
	PaymentMobileMoney PaymentMethod = "06"
	PaymentOther       PaymentMethod = "07"
)

// SalesStatus is the transaction progress code (§4.12).
type SalesStatus string

const (
	SalesWaitingApproval SalesStatus = "01"
	SalesApproved        SalesStatus = "02"
	SalesCancelRequested SalesStatus = "03"
	SalesCancelled       SalesStatus = "04"
	SalesCreditNoted     SalesStatus = "05"
	SalesTransferred     SalesStatus = "06"
)

// CreditNoteReason is the refund reason code (§4.17).
type CreditNoteReason string

const (
	ReasonMissingQuantity     CreditNoteReason = "01"
	ReasonMissingData         CreditNoteReason = "02"
	ReasonDamaged             CreditNoteReason = "03"
	ReasonWasted              CreditNoteReason = "04"
	ReasonRawMaterialShortage CreditNoteReason = "05"
	ReasonRefund              CreditNoteReason = "06"
)

// Qty is a quantity in hundredths of a unit: KRA's qty is NUMBER(13,2).
// Units(2) is 2.00; a weighed 1.85 kg line is Qty(185).
type Qty int64

// Units builds a whole-unit quantity.
func Units(n int64) Qty { return Qty(n * 100) }

// String renders the quantity with two decimals ("1.85").
func (q Qty) String() string {
	neg := ""
	if q < 0 {
		neg, q = "-", -q
	}

	return fmt.Sprintf("%s%d.%02d", neg, int64(q)/100, int64(q)%100)
}

// Party is a buyer or a seller: a KRA PIN and a name. The buyer is optional
// on a consumer sale; a buyer who wants to deduct the expense or claim input
// VAT needs their PIN on the invoice.
type Party struct {
	PIN   string
	Name  string
	Phone string
}

// Branch is a registered place of supply under a taxpayer's PIN. KRA
// registers one PIN per legal entity and a two-character branch id per
// branch: "00" is head office, "01" onwards are branches. Devices, item
// registrations and invoice sequences are all per branch.
type Branch struct {
	PIN      string
	BranchID string
}

// Device is an initialized control unit for one branch: the serial the
// application chose and the communication key KRA issued for it. The key
// is bound to the serial; initializing the same branch with another serial
// invalidates it.
type Device struct {
	Branch           Branch
	SerialNumber     string
	CommunicationKey string
}

// Item is a product or service a taxpayer registers with KRA once, before
// it can appear on an invoice line.
type Item struct {
	// Code is the taxpayer's unique item code (itemCd), composed with
	// ItemCode.
	Code string
	// ClassCode is the 10-digit UNSPSC-derived classification (itemClsCd)
	// from KRA's item class list.
	ClassCode string
	Name      string
	Type      ProductType
	// OriginCountry is an ISO alpha-2 code ("KE").
	OriginCountry string
	// PackagingUnit and QuantityUnit are codes from KRA's lists (§4.6, §4.7):
	// "NT" net, "U" unit, "KGM" kilogram, "LTR" liter.
	PackagingUnit string
	QuantityUnit  string
	TaxType       TaxType
	Barcode       string
	// DefaultPriceCents is the unit price KRA stores as dftPrc.
	DefaultPriceCents int64
	// Active maps to useYn; a delisted item is registered inactive, never
	// deleted.
	Active bool
}

// ItemCodeLength is the fixed width of a composed item code; the sequence
// is zero-padded to fill it, so a one-letter quantity unit gets an eight-digit
// sequence and a two-letter one gets seven.
const ItemCodeLength = 14

// ItemCode composes a KRA item code (§4.19): country, product type,
// packaging unit, quantity unit and a per-taxpayer sequence, for example
// KE2NTBA0000012 (Kenya, finished product, net, barrel, item 12) or
// KE3NTU00000042 (Kenya, service, net, unit, item 42).
func ItemCode(country string, productType ProductType, packagingUnit, quantityUnit string, seq int64) string {
	prefix := country + string(productType) + packagingUnit + quantityUnit

	width := ItemCodeLength - len(prefix)
	if width < 1 {
		width = 1
	}

	return fmt.Sprintf("%s%0*d", prefix, width, seq)
}

// Line is one invoice line: the item as sold, at a VAT-inclusive unit price.
type Line struct {
	ItemCode  string
	ClassCode string
	Name      string
	Barcode   string
	// PackagingUnit, Packages, QuantityUnit mirror the registered item;
	// Packages defaults to Quantity when zero.
	PackagingUnit string
	Packages      Qty
	QuantityUnit  string
	Quantity      Qty
	// UnitPriceCents is the VAT-inclusive price per unit.
	UnitPriceCents int64
	// DiscountCents is the discount on the whole line, already applied to
	// what the customer pays.
	DiscountCents int64
	TaxType       TaxType
}

// LineTotals is what KRA wants per line: the supply amount (quantity times
// price), the taxable amount after discount, the tax contained in it, and
// the line total.
type LineTotals struct {
	SupplyCents  int64
	TaxableCents int64
	TaxCents     int64
	TotalCents   int64
	// DiscountRateBps is the discount as basis points of the supply amount,
	// which is how KRA reports it (dcRt).
	DiscountRateBps int64
}

// Totals computes the line's amounts. Quantity is in hundredths, so the
// supply amount is price × quantity / 100, rounded half up to the cent.
func (l *Line) Totals(rates map[TaxType]int64) LineTotals {
	supply := roundHalfUpDiv(l.UnitPriceCents*int64(l.Quantity), 100)
	taxable := supply - l.DiscountCents

	var rateBps int64
	if rates != nil {
		rateBps = rates[l.TaxType]
	} else {
		rateBps = l.TaxType.RateBps()
	}

	tax := TaxContained(taxable, rateBps)

	var dcRate int64
	if supply > 0 && l.DiscountCents > 0 {
		dcRate = roundHalfUpDiv(l.DiscountCents*10_000, supply)
	}

	return LineTotals{SupplyCents: supply, TaxableCents: taxable, TaxCents: tax, TotalCents: taxable, DiscountRateBps: dcRate}
}

// TaxContained is the VAT inside a VAT-inclusive amount at rateBps, rounded
// half up to the cent: 16% inside KES 19.50 is KES 2.69, as KRA prints it.
func TaxContained(grossCents, rateBps int64) int64 {
	if rateBps <= 0 || grossCents == 0 {
		return 0
	}

	return roundHalfUpDiv(grossCents*rateBps, 10_000+rateBps)
}

// roundHalfUpDiv divides and rounds half away from zero, in integers.
func roundHalfUpDiv(num, den int64) int64 {
	if den == 0 {
		return 0
	}

	q, r := num/den, num%den
	if r < 0 {
		r = -r
	}

	if 2*r >= den {
		if (num < 0) != (den < 0) {
			return q - 1
		}

		return q + 1
	}

	return q
}

// TaxBucket is one row of the per-tax-type totals on an invoice header.
type TaxBucket struct {
	TaxableCents int64
	TaxCents     int64
	RateBps      int64
}

// InvoiceTotals is the header arithmetic KRA checks against the lines.
type InvoiceTotals struct {
	ByTaxType    map[TaxType]TaxBucket
	TaxableCents int64
	TaxCents     int64
	TotalCents   int64
	ItemCount    int
}

// Invoice is a sale or a credit note from one branch.
type Invoice struct {
	// InvoiceNo is the per-branch sequence KRA requires: it starts at 1 and
	// may not skip. The application allocates it from its database at
	// transmission time; the SDK never invents one.
	InvoiceNo int64
	// OriginalInvoiceNo is the InvoiceNo a credit note corrects; zero on a
	// sale.
	OriginalInvoiceNo int64
	// TraderInvoiceNo is the application's own reference (an order number).
	TraderInvoiceNo string
	Buyer           *Party
	ReceiptType     ReceiptType
	TransactionType TransactionType
	PaymentMethod   PaymentMethod
	Status          SalesStatus
	// ConfirmedAt is when the sale was confirmed (cfmDt); SalesDate derives
	// from it when zero.
	ConfirmedAt time.Time
	SalesDate   time.Time
	Lines       []Line
	// CreditNoteReason and RefundedAt are required on a credit note.
	CreditNoteReason CreditNoteReason
	RefundedAt       time.Time
	Remark           string
	// Rates overrides DefaultRates for this invoice; nil uses the defaults.
	Rates map[TaxType]int64
	// Receipt is what prints on the customer's copy.
	Receipt Receipt
}

// Receipt is the customer-facing part of the submission (trade name,
// address, header and footer lines, whether the purchaser accepted).
type Receipt struct {
	TradeName     string
	Address       string
	TopMessage    string
	BottomMessage string
	// PublishedAt is when the receipt was issued (rcptPbctDt); ConfirmedAt
	// is used when zero.
	PublishedAt time.Time
}

// Totals computes the header amounts from the lines.
func (inv *Invoice) Totals() InvoiceTotals {
	out := InvoiceTotals{ByTaxType: map[TaxType]TaxBucket{}, ItemCount: len(inv.Lines)}

	for _, t := range AllTaxTypes {
		out.ByTaxType[t] = TaxBucket{RateBps: inv.rateFor(t)}
	}

	for i := range inv.Lines {
		l := &inv.Lines[i]
		lt := l.Totals(inv.Rates)
		b := out.ByTaxType[l.TaxType]
		b.TaxableCents += lt.TaxableCents
		b.TaxCents += lt.TaxCents
		out.ByTaxType[l.TaxType] = b
		out.TaxableCents += lt.TaxableCents
		out.TaxCents += lt.TaxCents
		out.TotalCents += lt.TotalCents
	}

	return out
}

func (inv *Invoice) rateFor(t TaxType) int64 {
	if inv.Rates != nil {
		return inv.Rates[t]
	}

	return t.RateBps()
}

// Validate checks what every route would reject, before anything is sent.
// Failures wrap ErrInvalidInvoice.
func (inv *Invoice) Validate() error {
	if inv.InvoiceNo <= 0 {
		return fmt.Errorf("%w: invoice number must be allocated (>= 1)", ErrInvalidInvoice)
	}

	if len(inv.Lines) == 0 {
		return fmt.Errorf("%w: no lines", ErrInvalidInvoice)
	}

	switch inv.ReceiptType {
	case ReceiptSale:
		if inv.OriginalInvoiceNo != 0 {
			return fmt.Errorf("%w: a sale does not reference an original invoice", ErrInvalidInvoice)
		}
	case ReceiptCreditNote:
		if inv.OriginalInvoiceNo <= 0 {
			return fmt.Errorf("%w: a credit note must name the original invoice number", ErrInvalidInvoice)
		}

		if inv.CreditNoteReason == "" {
			return fmt.Errorf("%w: a credit note needs a reason code", ErrInvalidInvoice)
		}
	default:
		return fmt.Errorf("%w: receipt type must be S or R, got %q", ErrInvalidInvoice, inv.ReceiptType)
	}

	if inv.ConfirmedAt.IsZero() {
		return fmt.Errorf("%w: confirmed_at is required", ErrInvalidInvoice)
	}

	for i := range inv.Lines {
		l := &inv.Lines[i]
		if l.ItemCode == "" || l.Name == "" {
			return fmt.Errorf("%w: line %d needs an item code and a name", ErrInvalidInvoice, i+1)
		}

		if l.Quantity <= 0 {
			return fmt.Errorf("%w: line %d quantity must be positive", ErrInvalidInvoice, i+1)
		}

		if l.UnitPriceCents < 0 || l.DiscountCents < 0 {
			return fmt.Errorf("%w: line %d has a negative amount", ErrInvalidInvoice, i+1)
		}

		if !l.TaxType.Valid() {
			return fmt.Errorf("%w: line %d tax type %q is not one of A to E", ErrInvalidInvoice, i+1, l.TaxType)
		}

		if lt := l.Totals(inv.Rates); lt.TaxableCents < 0 {
			return fmt.Errorf("%w: line %d discount exceeds the supply amount", ErrInvalidInvoice, i+1)
		}
	}

	return nil
}

// Signature is what KRA returns when it signs an invoice: the five values
// that make it legal and must be stored against the invoice and printed on
// the receipt.
type Signature struct {
	// ReceiptNo is the control unit's receipt number for this invoice
	// (curRcptNo) and TotalReceiptNo its running count (totRcptNo).
	ReceiptNo      int64
	TotalReceiptNo int64
	InternalData   string
	ReceiptSign    string
	// SignedAt is the control unit's timestamp (sdcDateTime).
	SignedAt time.Time
}

// Verification hosts for the QR link on a receipt.
const (
	VerificationHostProduction = "etims.kra.go.ke"
	VerificationHostSandbox    = "etims-sbx.kra.go.ke"
)

// VerificationURL is the link KRA's receipt QR code encodes: the seller's
// PIN, the branch id and the receipt signature, concatenated.
func (s Signature) VerificationURL(host string, b Branch) string {
	data := url.QueryEscape(b.PIN + b.BranchID + s.ReceiptSign)

	return "https://" + host + "/common/link/etims/receipt/indexEtimsReceiptData?Data=" + data
}

// ItemClass is one row of KRA's item classification list (UNSPSC-derived).
type ItemClass struct {
	Code    string
	Name    string
	Level   int
	TaxType TaxType // KRA's hint; empty when KRA gives none
	Active  bool
}

// Code is one entry of a KRA code list (tax types, units, payment methods).
type Code struct {
	Class       string
	ClassName   string
	Code        string
	Name        string
	Description string
	SortOrder   int
	// UserDefined1 carries the rate for tax types ("16").
	UserDefined1 string
	Active       bool
}

// BranchInfo is one branch registered on a PIN.
type BranchInfo struct {
	Branch     Branch
	Name       string
	StatusCode string
	County     string
	SubCounty  string
	Locality   string
	Location   string
	Manager    string
	HeadOffice bool
}

// Invoicer submits invoices for one branch.
type Invoicer interface {
	// SubmitInvoice sends a sale or a credit note and returns KRA's
	// signature. Errors wrap ErrInvalidInvoice, ErrDuplicate, ErrRejected
	// or ErrUnavailable; see each for what the caller should do.
	SubmitInvoice(ctx context.Context, inv *Invoice) (Signature, error)
}

// ItemRegistrar registers items for one branch.
type ItemRegistrar interface {
	RegisterItem(ctx context.Context, item *Item) error
}

// Catalog reads KRA's reference lists for one branch. Lists are
// incremental: since is the last request time, and the zero time asks for
// everything.
type Catalog interface {
	ItemClasses(ctx context.Context, since time.Time) ([]ItemClass, error)
	Codes(ctx context.Context, since time.Time) ([]Code, error)
	Branches(ctx context.Context, since time.Time) ([]BranchInfo, error)
}
