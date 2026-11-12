package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kennankole/drop-eTIMS-sdk/etims"
	"github.com/kennankole/drop-eTIMS-sdk/etims/fake"
)

func sale(no int64) etims.Invoice {
	return etims.Invoice{
		InvoiceNo: no, TraderInvoiceNo: "ORD-1", ReceiptType: etims.ReceiptSale, PaymentMethod: etims.PaymentMobileMoney,
		ConfirmedAt: time.Now(),
		Lines: []etims.Line{{ItemCode: "KE3NTU00000001", ClassCode: "5059690800", Name: "Delivery fee",
			Quantity: etims.Units(1), UnitPriceCents: 13_000, TaxType: etims.TaxStandard}},
	}
}

func TestFakeSignsOnceAndRefusesReuse(t *testing.T) {
	r := fake.New()
	ctx := context.Background()

	sig, err := r.SubmitInvoice(ctx, ptr(sale(1)))
	require.NoError(t, err)
	require.Len(t, sig.ReceiptSign, 16)
	require.Len(t, sig.InternalData, 26)
	require.EqualValues(t, 1, sig.ReceiptNo)

	again, err := r.SubmitInvoice(ctx, ptr(sale(1)))
	require.ErrorIs(t, err, etims.ErrDuplicate)
	require.Zero(t, again)

	stored, ok := r.Signature(1)
	require.True(t, ok)
	require.Equal(t, sig, stored)
	require.Len(t, r.Sent(), 1)
}

func TestFakeValidatesAndFails(t *testing.T) {
	r := fake.New()
	ctx := context.Background()

	_, err := r.SubmitInvoice(ctx, &etims.Invoice{})
	require.ErrorIs(t, err, etims.ErrInvalidInvoice)

	r.Fail = etims.ErrUnavailable
	_, err = r.SubmitInvoice(ctx, ptr(sale(2)))
	require.ErrorIs(t, err, etims.ErrUnavailable)
	require.ErrorIs(t, r.RegisterItem(ctx, &etims.Item{Code: "x"}), etims.ErrUnavailable)

	r.Fail = nil
	require.NoError(t, r.RegisterItem(ctx, &etims.Item{Code: "KE3NTU00000001", Name: "Delivery fee"}))
	require.Contains(t, r.Items(), "KE3NTU00000001")
}

// ptr takes the address of a freshly built invoice fixture.
func ptr(i etims.Invoice) *etims.Invoice { return &i } //nolint:gocritic // hugeParam: a test fixture copied once per case
