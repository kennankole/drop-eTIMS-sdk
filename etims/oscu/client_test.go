package oscu_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kennankole/drop-eTIMS-sdk/etims"
	"github.com/kennankole/drop-eTIMS-sdk/etims/oscu"
)

var nairobi = time.FixedZone("EAT", 3*60*60)

type capture struct {
	path    string
	headers http.Header
	body    map[string]any
}

// kraServer answers every path from the handler map with the JSON given,
// recording what it received.
func kraServer(t *testing.T, answers map[string]string, status int) (*httptest.Server, *[]capture) {
	t.Helper()

	var got []capture

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		var body map[string]any
		_ = json.Unmarshal(raw, &body)

		got = append(got, capture{path: r.URL.Path, headers: r.Header.Clone(), body: body})

		w.WriteHeader(status)
		_, _ = w.Write([]byte(answers[r.URL.Path]))
	}))
	t.Cleanup(srv.Close)

	return srv, &got
}

const initAnswer = `{"resultCd":"000","resultMsg":"It is succeeded","resultDt":"20200226143124",
"data":{"info":{"tin":"A123456789Z","taxprNm":"Taxpayer1130","bhfId":"00","bhfNm":"Headquarters","hqYn":"Y",
"dvcId":"9999911300000001","sdcId":"KRACU013000001","mrcNo":"WIS01000150","cmcKey":"f0b9831bd2334874b7ec815e40347bc4"}}}`

const salesAnswer = `{"resultCd":"000","resultMsg":"It is succeeded","resultDt":"20200226194328",
"data":{"curRcptNo":"7","totRcptNo":"7","intrlData":"EAHSAV6ECUUXSY6PCCJYAUP6MI","rcptSign":"QUII27MATATSHFRB","sdcDateTime":"20261112103015"}}`

func device() etims.Device {
	return etims.Device{Branch: etims.Branch{PIN: "A123456789Z", BranchID: "00"}, SerialNumber: "DROP-0001", CommunicationKey: "f0b9831bd2334874b7ec815e40347bc4"}
}

func sale() etims.Invoice {
	return etims.Invoice{
		InvoiceNo: 42, TraderInvoiceNo: "ORD-7F3A", ReceiptType: etims.ReceiptSale, PaymentMethod: etims.PaymentMobileMoney,
		Buyer:       &etims.Party{PIN: "A001234567Z", Name: "Jane", Phone: "0712345678"},
		ConfirmedAt: time.Date(2026, 11, 12, 7, 30, 0, 0, time.UTC), // 10:30 in Nairobi
		Lines: []etims.Line{
			{ItemCode: "KE3NTU00000001", ClassCode: "5059690800", Name: "Delivery fee", Quantity: etims.Units(1), UnitPriceCents: 13_000, TaxType: etims.TaxStandard, PackagingUnit: "NT", QuantityUnit: "U"},
			{ItemCode: "KE3NTU00000002", ClassCode: "5059690800", Name: "Service fee", Quantity: etims.Units(1), UnitPriceCents: 5_000, TaxType: etims.TaxStandard, PackagingUnit: "NT", QuantityUnit: "U"},
		},
		Receipt: etims.Receipt{TradeName: "Drop", Address: "Nairobi"},
	}
}

func TestInitializeReturnsTheCommunicationKey(t *testing.T) {
	srv, got := kraServer(t, map[string]string{"/selectInitOsdcInfo": initAnswer}, http.StatusOK)
	c := oscu.NewClient(srv.URL, nil, oscu.WithLocation(nairobi))

	info, err := c.Initialize(context.Background(), etims.Branch{PIN: "A123456789Z", BranchID: "00"}, "DROP-0001")
	require.NoError(t, err)
	assert.Equal(t, "f0b9831bd2334874b7ec815e40347bc4", info.Device.CommunicationKey)
	assert.Equal(t, "DROP-0001", info.Device.SerialNumber)
	assert.True(t, info.HeadOffice)
	assert.Equal(t, "KRACU013000001", info.SDCID)

	require.Len(t, *got, 1)
	req := (*got)[0]
	assert.Equal(t, "/selectInitOsdcInfo", req.path)
	assert.Empty(t, req.headers.Get("cmcKey"), "initialization is the one call without a key")
	assert.Equal(t, map[string]any{"tin": "A123456789Z", "bhfId": "00", "dvcSrlNo": "DROP-0001"}, req.body)
}

func TestSubmitInvoiceSendsTheSpecificationShapeAndParsesTheSignature(t *testing.T) {
	srv, got := kraServer(t, map[string]string{"/saveTrnsSalesOsdc": salesAnswer}, http.StatusOK)
	c := oscu.NewClient(srv.URL, nil, oscu.WithLocation(nairobi))
	b := oscu.NewBranchClient(c, device())

	sig, err := b.SubmitInvoice(context.Background(), ptr(sale()))
	require.NoError(t, err)
	assert.Equal(t, etims.Signature{
		ReceiptNo: 7, TotalReceiptNo: 7, InternalData: "EAHSAV6ECUUXSY6PCCJYAUP6MI", ReceiptSign: "QUII27MATATSHFRB",
		SignedAt: time.Date(2026, 11, 12, 10, 30, 15, 0, nairobi),
	}, sig)

	req := (*got)[0]
	assert.Equal(t, "A123456789Z", req.headers.Get("tin"))
	assert.Equal(t, "00", req.headers.Get("bhfId"))
	assert.Equal(t, "f0b9831bd2334874b7ec815e40347bc4", req.headers.Get("cmcKey"))

	body := req.body
	assert.EqualValues(t, 42, body["invcNo"])
	assert.EqualValues(t, 0, body["orgInvcNo"])
	assert.Equal(t, "ORD-7F3A", body["trdInvcNo"])
	assert.Equal(t, "S", body["rcptTyCd"])
	assert.Equal(t, "N", body["salesTyCd"])
	assert.Equal(t, "06", body["pmtTyCd"])
	assert.Equal(t, "02", body["salesSttsCd"])
	assert.Equal(t, "20261112103000", body["cfmDt"], "timestamps are Nairobi wall-clock")
	assert.Equal(t, "20261112", body["salesDt"])
	assert.Equal(t, "A001234567Z", body["custTin"])
	assert.EqualValues(t, 2, body["totItemCnt"])

	// 130.00 + 50.00 of VAT-inclusive fees: 180.00 taxable at B, VAT 24.83.
	assert.EqualValues(t, 180.00, body["taxblAmtB"])
	assert.EqualValues(t, 17.93+6.90, body["taxAmtB"])
	assert.EqualValues(t, 16.00, body["taxRtB"])
	assert.EqualValues(t, 0, body["taxblAmtA"])
	assert.EqualValues(t, 180.00, body["totTaxblAmt"])
	assert.EqualValues(t, 24.83, body["totTaxAmt"])
	assert.EqualValues(t, 180.00, body["totAmt"])
	assert.Nil(t, body["rfdRsnCd"])

	lines, ok := body["itemList"].([]any)
	require.True(t, ok)
	require.Len(t, lines, 2)
	first, _ := lines[0].(map[string]any)
	assert.EqualValues(t, 1, first["itemSeq"])
	assert.Equal(t, "KE3NTU00000001", first["itemCd"])
	assert.EqualValues(t, 1.00, first["qty"])
	assert.EqualValues(t, 130.00, first["prc"])
	assert.EqualValues(t, 130.00, first["splyAmt"])
	assert.EqualValues(t, 17.93, first["taxAmt"])
	assert.EqualValues(t, 130.00, first["totAmt"])
	assert.Equal(t, "B", first["taxTyCd"])

	receipt, _ := body["receipt"].(map[string]any)
	assert.Equal(t, "Drop", receipt["trdeNm"])
	assert.Equal(t, "0712345678", receipt["custMblNo"])
}

func TestSubmitCreditNoteNamesTheOriginal(t *testing.T) {
	srv, got := kraServer(t, map[string]string{"/saveTrnsSalesOsdc": salesAnswer}, http.StatusOK)
	b := oscu.NewBranchClient(oscu.NewClient(srv.URL, nil, oscu.WithLocation(nairobi)), device())

	cn := sale()
	cn.InvoiceNo, cn.ReceiptType, cn.OriginalInvoiceNo, cn.CreditNoteReason = 43, etims.ReceiptCreditNote, 42, etims.ReasonRefund
	cn.RefundedAt = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	_, err := b.SubmitInvoice(context.Background(), &cn)
	require.NoError(t, err)

	body := (*got)[0].body
	assert.Equal(t, "R", body["rcptTyCd"])
	assert.EqualValues(t, 42, body["orgInvcNo"])
	assert.Equal(t, "06", body["rfdRsnCd"])
	assert.Equal(t, "20261112120000", body["rfdDt"])
}

func TestSubmitInvoiceMapsResultCodes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"overlapped data is a duplicate", http.StatusOK, `{"resultCd":"994","resultMsg":"There is an overlapped Data","resultDt":"x"}`, etims.ErrDuplicate},
		{"parameter error is a rejection", http.StatusOK, `{"resultCd":"910","resultMsg":"Request parameter error","resultDt":"x"}`, etims.ErrRejected},
		{"invalid device is a rejection", http.StatusOK, `{"resultCd":"901","resultMsg":"It is not valid device","resultDt":"x"}`, etims.ErrRejected},
		{"unknown error is retryable", http.StatusOK, `{"resultCd":"999","resultMsg":"unknown","resultDt":"x"}`, etims.ErrUnavailable},
		{"a 5xx is retryable", http.StatusBadGateway, `<html>bad gateway</html>`, etims.ErrUnavailable},
		{"garbage is retryable, never a signature", http.StatusOK, `{"ok":true}`, etims.ErrUnavailable},
		{"success without a signature is retryable", http.StatusOK, `{"resultCd":"000","resultMsg":"ok","resultDt":"x","data":{"curRcptNo":"1"}}`, etims.ErrUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := kraServer(t, map[string]string{"/saveTrnsSalesOsdc": tc.body}, tc.status)
			b := oscu.NewBranchClient(oscu.NewClient(srv.URL, nil), device())

			_, err := b.SubmitInvoice(context.Background(), ptr(sale()))
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.want), "got %v", err)

			var re *etims.ResultError
			if errors.As(err, &re) {
				assert.NotEmpty(t, re.Code)
			}
		})
	}
}

func TestSubmitInvoiceNeverSendsAnInvalidInvoice(t *testing.T) {
	srv, got := kraServer(t, nil, http.StatusOK)
	b := oscu.NewBranchClient(oscu.NewClient(srv.URL, nil), device())

	_, err := b.SubmitInvoice(context.Background(), &etims.Invoice{})
	require.ErrorIs(t, err, etims.ErrInvalidInvoice)
	assert.Empty(t, *got)

	_, err = oscu.NewBranchClient(oscu.NewClient(srv.URL, nil), etims.Device{}).SubmitInvoice(context.Background(), ptr(sale()))
	require.ErrorIs(t, err, etims.ErrNotInitialized)
}

func TestRegisterItem(t *testing.T) {
	srv, got := kraServer(t, map[string]string{"/saveItem": `{"resultCd":"000","resultMsg":"It is succeeded","resultDt":"x","data":null}`}, http.StatusOK)
	b := oscu.NewBranchClient(oscu.NewClient(srv.URL, nil), device())

	err := b.RegisterItem(context.Background(), &etims.Item{
		Code: etims.ItemCode("KE", etims.ProductService, "NT", "U", 1), ClassCode: "5059690800", Name: "Delivery fee",
		Type: etims.ProductService, PackagingUnit: "NT", QuantityUnit: "U", TaxType: etims.TaxStandard, DefaultPriceCents: 13_000, Active: true,
	})
	require.NoError(t, err)

	body := (*got)[0].body
	assert.Equal(t, "KE3NTU00000001", body["itemCd"])
	assert.Equal(t, "3", body["itemTyCd"])
	assert.Equal(t, "B", body["taxTyCd"])
	assert.Equal(t, "KE", body["orgnNatCd"])
	assert.EqualValues(t, 130.00, body["dftPrc"])
	assert.Equal(t, "Y", body["useYn"])
	assert.Equal(t, "N", body["isrcAplcbYn"])
}

func TestCatalogLists(t *testing.T) {
	answers := map[string]string{
		"/selectItemClsList": `{"resultCd":"000","resultMsg":"ok","resultDt":"x","data":{"itemClsList":[{"itemClsCd":"3133130600","itemClsNm":"Welded assemblies","itemClsLvl":5,"taxTyCd":"B","mjrTgYn":"N","useYn":"Y"}]}}`,
		"/selectCodeList":    `{"resultCd":"000","resultMsg":"ok","resultDt":"x","data":{"clsList":[{"cdCls":"04","cdClsNm":"Taxation Type","useYn":"Y","dtlList":[{"cd":"B","cdNm":"B-16.00%","cdDesc":"B-16.00%","useYn":"Y","srtOrd":2,"userDfnCd1":"16"}]}]}}`,
		"/selectBhfList":     `{"resultCd":"001","resultMsg":"There is no search result","resultDt":"x"}`,
	}
	srv, got := kraServer(t, answers, http.StatusOK)
	b := oscu.NewBranchClient(oscu.NewClient(srv.URL, nil, oscu.WithLocation(nairobi)), device())

	classes, err := b.ItemClasses(context.Background(), time.Time{})
	require.NoError(t, err)
	require.Equal(t, []etims.ItemClass{{Code: "3133130600", Name: "Welded assemblies", Level: 5, TaxType: etims.TaxStandard, Active: true}}, classes)
	assert.Equal(t, "20180101000000", (*got)[0].body["lastReqDt"], "the zero time asks for everything")

	codes, err := b.Codes(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, nairobi))
	require.NoError(t, err)
	require.Equal(t, []etims.Code{{Class: "04", ClassName: "Taxation Type", Code: "B", Name: "B-16.00%", Description: "B-16.00%", SortOrder: 2, UserDefined1: "16", Active: true}}, codes)
	assert.Equal(t, "20260101000000", (*got)[1].body["lastReqDt"])

	branches, err := b.Branches(context.Background(), time.Time{})
	require.NoError(t, err, "001 on a list is an empty page, not a failure")
	assert.Empty(t, branches)
}

func TestObserverSeesEveryCall(t *testing.T) {
	srv, _ := kraServer(t, map[string]string{"/saveTrnsSalesOsdc": `{"resultCd":"910","resultMsg":"bad","resultDt":"x"}`}, http.StatusOK)

	var calls atomic.Int32
	var last etims.Event

	c := oscu.NewClient(srv.URL, nil, oscu.WithObserver(etims.ObserverFunc(func(e *etims.Event) { calls.Add(1); last = *e })))
	_, err := oscu.NewBranchClient(c, device()).SubmitInvoice(context.Background(), ptr(sale()))
	require.Error(t, err)

	assert.EqualValues(t, 1, calls.Load())
	assert.Equal(t, "submit_invoice", last.Operation)
	assert.Equal(t, "910", last.ResultCode)
	assert.Equal(t, "00", last.Branch.BranchID)
	assert.ErrorIs(t, last.Err, etims.ErrRejected)
}

// ptr takes the address of a freshly built invoice fixture.
func ptr(i etims.Invoice) *etims.Invoice { return &i } //nolint:gocritic // hugeParam: a test fixture copied once per case
