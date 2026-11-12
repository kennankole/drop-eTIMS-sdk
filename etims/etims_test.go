package etims_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kennankole/drop-eTIMS-sdk/etims"
)

// The arithmetic KRA prints: VAT is the part contained in a VAT-inclusive
// price, rounded half up. A KES 19.50 fee carries KES 2.69; KRA's own
// sample, 10,500 at 18%, carries 1,602.
func TestTaxContained(t *testing.T) {
	assert.EqualValues(t, 269, etims.TaxContained(1_950, 1_600))
	assert.EqualValues(t, 2_600, etims.TaxContained(18_850, 1_600))
	assert.EqualValues(t, 160_169, etims.TaxContained(1_050_000, 1_800), "KRA prints 1,602 because its sample rounds to whole shillings; in cents it is 1,601.69")
	assert.Zero(t, etims.TaxContained(1_950, 0), "exempt, zero-rated and non-VAT carry nothing")
	assert.Zero(t, etims.TaxContained(0, 1_600))
}

func TestLineTotals(t *testing.T) {
	l := etims.Line{Quantity: etims.Units(2), UnitPriceCents: 3_500 * 100, TaxType: etims.TaxStandard, DiscountCents: 70_000}
	lt := l.Totals(nil)

	assert.EqualValues(t, 700_000, lt.SupplyCents, "2 x 3,500.00")
	assert.EqualValues(t, 630_000, lt.TaxableCents, "after a 700.00 discount")
	assert.EqualValues(t, 630_000, lt.TotalCents)
	assert.EqualValues(t, etims.TaxContained(630_000, 1_600), lt.TaxCents)
	assert.EqualValues(t, 1_000, lt.DiscountRateBps, "10%")

	weighed := etims.Line{Quantity: 185, UnitPriceCents: 89_900, TaxType: etims.TaxZeroRated} // 1.85 kg at 899.00/kg
	assert.EqualValues(t, 166_315, weighed.Totals(nil).SupplyCents, "1.85 x 899.00 = 1,663.15")
	assert.Zero(t, weighed.Totals(nil).TaxCents)
}

func TestInvoiceTotalsBucketsByTaxType(t *testing.T) {
	inv := etims.Invoice{Lines: []etims.Line{
		{Quantity: etims.Units(1), UnitPriceCents: 13_000, TaxType: etims.TaxStandard},  // delivery 130.00
		{Quantity: etims.Units(1), UnitPriceCents: 5_000, TaxType: etims.TaxStandard},   // service 50.00
		{Quantity: etims.Units(2), UnitPriceCents: 12_000, TaxType: etims.TaxZeroRated}, // 2 x milk 120.00
		{Quantity: etims.Units(1), UnitPriceCents: 30_000, TaxType: etims.TaxExempt},    // medicine 300.00
	}}

	tot := inv.Totals()
	assert.Equal(t, 4, tot.ItemCount)
	assert.EqualValues(t, 18_000, tot.ByTaxType[etims.TaxStandard].TaxableCents)
	assert.EqualValues(t, etims.TaxContained(13_000, 1_600)+etims.TaxContained(5_000, 1_600), tot.ByTaxType[etims.TaxStandard].TaxCents)
	assert.EqualValues(t, 24_000, tot.ByTaxType[etims.TaxZeroRated].TaxableCents)
	assert.Zero(t, tot.ByTaxType[etims.TaxZeroRated].TaxCents)
	assert.EqualValues(t, 30_000, tot.ByTaxType[etims.TaxExempt].TaxableCents)
	assert.EqualValues(t, 1_600, tot.ByTaxType[etims.TaxStandard].RateBps)
	assert.EqualValues(t, 72_000, tot.TotalCents)
	assert.Equal(t, tot.TaxCents, tot.ByTaxType[etims.TaxStandard].TaxCents, "only B carries VAT here")
}

func TestItemCode(t *testing.T) {
	assert.Equal(t, "KE2NTBA0000012", etims.ItemCode("KE", etims.ProductFinished, "NT", "BA", 12))
	assert.Equal(t, "KE3NTU00000042", etims.ItemCode("KE", etims.ProductService, "NT", "U", 42), "Glovo's delivery fee item")
}

func TestQtyString(t *testing.T) {
	assert.Equal(t, "2.00", etims.Units(2).String())
	assert.Equal(t, "1.85", etims.Qty(185).String())
	assert.Equal(t, "0.05", etims.Qty(5).String())
}

func validSale() etims.Invoice {
	return etims.Invoice{
		InvoiceNo: 1, TraderInvoiceNo: "ORD-1", ReceiptType: etims.ReceiptSale, PaymentMethod: etims.PaymentMobileMoney,
		ConfirmedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
		Lines: []etims.Line{{ItemCode: "KE3NTU00000001", ClassCode: "5059690800", Name: "Delivery fee", Quantity: etims.Units(1),
			UnitPriceCents: 13_000, TaxType: etims.TaxStandard, PackagingUnit: "NT", QuantityUnit: "U"}},
	}
}

func TestInvoiceValidate(t *testing.T) {
	ok := validSale()
	require.NoError(t, ok.Validate())

	cases := map[string]func(*etims.Invoice){
		"unallocated number":      func(i *etims.Invoice) { i.InvoiceNo = 0 },
		"no lines":                func(i *etims.Invoice) { i.Lines = nil },
		"sale naming an original": func(i *etims.Invoice) { i.OriginalInvoiceNo = 7 },
		"credit note without original": func(i *etims.Invoice) {
			i.ReceiptType = etims.ReceiptCreditNote
			i.CreditNoteReason = etims.ReasonRefund
		},
		"credit note without reason":    func(i *etims.Invoice) { i.ReceiptType = etims.ReceiptCreditNote; i.OriginalInvoiceNo = 1 },
		"unknown receipt type":          func(i *etims.Invoice) { i.ReceiptType = "X" },
		"zero quantity":                 func(i *etims.Invoice) { i.Lines[0].Quantity = 0 },
		"unknown tax type":              func(i *etims.Invoice) { i.Lines[0].TaxType = "F" },
		"discount larger than the line": func(i *etims.Invoice) { i.Lines[0].DiscountCents = 99_999 },
		"no confirmation time":          func(i *etims.Invoice) { i.ConfirmedAt = time.Time{} },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			inv := validSale()
			mutate(&inv)
			err := inv.Validate()
			require.Error(t, err)
			require.True(t, errors.Is(err, etims.ErrInvalidInvoice), err)
		})
	}

	cn := validSale()
	cn.ReceiptType, cn.OriginalInvoiceNo, cn.CreditNoteReason = etims.ReceiptCreditNote, 1, etims.ReasonRefund
	require.NoError(t, cn.Validate())
}

func TestVerificationURL(t *testing.T) {
	sig := etims.Signature{ReceiptSign: "QUII27MATATSHFRB"}
	got := sig.VerificationURL(etims.VerificationHostSandbox, etims.Branch{PIN: "A123456789Z", BranchID: "00"})
	assert.Equal(t, "https://etims-sbx.kra.go.ke/common/link/etims/receipt/indexEtimsReceiptData?Data=A123456789Z00QUII27MATATSHFRB", got)
}

func TestResultErrorKeepsTheContract(t *testing.T) {
	err := &etims.ResultError{Code: "994", Message: "overlapped", Kind: etims.ErrDuplicate}
	require.ErrorIs(t, err, etims.ErrDuplicate)
	assert.Contains(t, err.Error(), "994")
}
