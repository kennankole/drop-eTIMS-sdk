// Package oscu talks to KRA's eTIMS Online Sales Control Unit API directly,
// as described in the OSCU Specification Document v2.0 (KRA, April 2023):
// device initialization, item registration, sales and credit notes, and the
// reference lists (codes, item classes, branches).
//
// Every call after initialization carries the branch's PIN (tin), branch id
// (bhfId) and communication key (cmcKey), both as HTTP headers and in the
// JSON body, which is how the specification's samples send them. Responses
// share one envelope: resultCd, resultMsg, resultDt and data. "000" is
// success; everything else is mapped onto the etims error contract
// (ErrDuplicate, ErrRejected, ErrUnavailable) so the application never
// compares KRA's codes itself.
//
// The request and response shapes follow the published specification and
// have not yet been exercised against KRA's sandbox. Anything the client
// does not recognize is reported as etims.ErrUnavailable, never as a signed
// invoice, with the raw body kept in the error.
package oscu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kennankole/drop-eTIMS-sdk/etims"
)

// Base URLs from the specification.
const (
	SandboxBaseURL    = "https://etims-api-sbx.kra.go.ke/etims-api"
	ProductionBaseURL = "https://etims-api.kra.go.ke/etims-api"
)

const (
	headerTIN      = "tin"
	headerBranchID = "bhfId"
	headerCmcKey   = "cmcKey"

	pathInitialize  = "/selectInitOsdcInfo"
	pathCodes       = "/selectCodeList"
	pathItemClasses = "/selectItemClsList"
	pathBranches    = "/selectBhfList"
	pathSaveItem    = "/saveItem"
	pathSaveSales   = "/saveTrnsSalesOsdc"

	// KRA's result codes the client acts on. Everything else that is not
	// "000" is a rejection.
	resultOK        = "000"
	resultNoResults = "001"
	resultDuplicate = "994"

	// Timestamps are yyyyMMddHHmmss; dates are yyyyMMdd.
	stampLayout = "20060102150405"
	dateLayout  = "20060102"

	maxBodyBytes  = 4 << 20
	maxDetailBody = 300
)

// Client is an OSCU API client bound to a base URL. It holds no branch
// credentials; see BranchClient. Safe for concurrent use.
type Client struct {
	baseURL  string
	http     *http.Client
	observer etims.Observer
	// Location is the time zone KRA timestamps are rendered in; KRA runs on
	// Nairobi time.
	loc *time.Location
}

// Option configures a Client.
type Option func(*Client)

// WithObserver reports every call to o.
func WithObserver(o etims.Observer) Option {
	return func(c *Client) {
		if o != nil {
			c.observer = o
		}
	}
}

// WithLocation sets the time zone timestamps are rendered in. Defaults to
// Africa/Nairobi, falling back to UTC+3 if the zone database is missing.
func WithLocation(loc *time.Location) Option {
	return func(c *Client) {
		if loc != nil {
			c.loc = loc
		}
	}
}

// NewClient builds a client. baseURL defaults to the sandbox; httpClient
// defaults to one with a 30 s timeout (KRA is slow and the call runs off the
// request path, from a worker).
func NewClient(baseURL string, httpClient *http.Client, opts ...Option) *Client {
	if baseURL == "" {
		baseURL = SandboxBaseURL
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	loc, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		loc = time.FixedZone("EAT", 3*60*60)
	}

	c := &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, observer: etims.NoopObserver, loc: loc}
	for _, o := range opts {
		o(c)
	}

	return c
}

// DeviceInfo is what KRA returns on initialization: the taxpayer, the branch
// and the device, including the communication key every later call needs.
type DeviceInfo struct {
	Device       etims.Device
	TaxpayerName string
	BranchName   string
	HeadOffice   bool
	DeviceID     string
	SDCID        string
	MRCNo        string
}

// Initialize registers a control unit for one branch and returns its
// communication key. It is the only call without a key. KRA binds the key
// to the serial: initializing the same branch again with another serial
// invalidates the first key, and KRA answers 902 ("this device is
// installed") when the same serial is initialized twice.
func (c *Client) Initialize(ctx context.Context, branch etims.Branch, serialNumber string) (DeviceInfo, error) {
	if branch.PIN == "" || branch.BranchID == "" || serialNumber == "" {
		return DeviceInfo{}, fmt.Errorf("%w: pin, branch id and serial number are required", etims.ErrInvalidInvoice)
	}

	req := map[string]string{headerTIN: branch.PIN, headerBranchID: branch.BranchID, "dvcSrlNo": serialNumber}

	var data struct {
		Info struct {
			TIN      string `json:"tin"`
			TaxprNm  string `json:"taxprNm"`
			BhfID    string `json:"bhfId"`
			BhfNm    string `json:"bhfNm"`
			HqYn     string `json:"hqYn"`
			DvcID    string `json:"dvcId"`
			SdcID    string `json:"sdcId"`
			MrcNo    string `json:"mrcNo"`
			CmcKey   string `json:"cmcKey"`
			Internal string `json:"-"`
		} `json:"info"`
	}

	if _, err := c.call(ctx, "initialize", branch, nil, pathInitialize, req, &data); err != nil {
		return DeviceInfo{}, err
	}

	if data.Info.CmcKey == "" {
		return DeviceInfo{}, fmt.Errorf("%w: initialization succeeded without a communication key", etims.ErrUnavailable)
	}

	return DeviceInfo{
		Device:       etims.Device{Branch: branch, SerialNumber: serialNumber, CommunicationKey: data.Info.CmcKey},
		TaxpayerName: data.Info.TaxprNm,
		BranchName:   data.Info.BhfNm,
		HeadOffice:   data.Info.HqYn == "Y",
		DeviceID:     data.Info.DvcID,
		SDCID:        data.Info.SdcID,
		MRCNo:        data.Info.MrcNo,
	}, nil
}

// BranchClient is the Client bound to one initialized device. It implements
// etims.Invoicer, etims.ItemRegistrar and etims.Catalog.
type BranchClient struct {
	c   *Client
	dev etims.Device
	// Registrar is the regrId/regrNm and modrId/modrNm KRA wants on every
	// write; the application's name.
	Registrar string
}

// NewBranchClient binds a client to a device. Registrar defaults to "drop".
func NewBranchClient(c *Client, dev etims.Device) *BranchClient {
	return &BranchClient{c: c, dev: dev, Registrar: "drop"}
}

// Device is the device this client is bound to.
func (b *BranchClient) Device() etims.Device { return b.dev }

func (b *BranchClient) ready() error {
	if b.dev.CommunicationKey == "" || b.dev.Branch.PIN == "" || b.dev.Branch.BranchID == "" {
		return etims.ErrNotInitialized
	}

	return nil
}

// RegisterItem registers (or updates, same code) an item for the branch.
func (b *BranchClient) RegisterItem(ctx context.Context, item *etims.Item) error {
	if err := b.ready(); err != nil {
		return err
	}

	if item.Code == "" || item.ClassCode == "" || item.Name == "" || !item.TaxType.Valid() {
		return fmt.Errorf("%w: item needs a code, a class code, a name and a tax type", etims.ErrInvalidInvoice)
	}

	useYn := "N"
	if item.Active {
		useYn = "Y"
	}

	origin := item.OriginCountry
	if origin == "" {
		origin = "KE"
	}

	req := map[string]any{
		"tin":         b.dev.Branch.PIN,
		"bhfId":       b.dev.Branch.BranchID,
		"itemCd":      item.Code,
		"itemClsCd":   item.ClassCode,
		"itemTyCd":    string(item.Type),
		"itemNm":      item.Name,
		"itemStdNm":   nil,
		"orgnNatCd":   origin,
		"pkgUnitCd":   item.PackagingUnit,
		"qtyUnitCd":   item.QuantityUnit,
		"taxTyCd":     string(item.TaxType),
		"btchNo":      nil,
		"bcd":         nullable(item.Barcode),
		"dftPrc":      money(item.DefaultPriceCents),
		"grpPrcL1":    money(item.DefaultPriceCents),
		"grpPrcL2":    money(item.DefaultPriceCents),
		"grpPrcL3":    money(item.DefaultPriceCents),
		"grpPrcL4":    money(item.DefaultPriceCents),
		"grpPrcL5":    nil,
		"addInfo":     nil,
		"sftyQty":     nil,
		"isrcAplcbYn": "N",
		"useYn":       useYn,
		"regrId":      b.Registrar,
		"regrNm":      b.Registrar,
		"modrId":      b.Registrar,
		"modrNm":      b.Registrar,
	}

	_, err := b.c.call(ctx, "register_item", b.dev.Branch, &b.dev, pathSaveItem, req, nil)

	return err
}

// SubmitInvoice sends a sale or a credit note and returns the signature.
func (b *BranchClient) SubmitInvoice(ctx context.Context, inv *etims.Invoice) (etims.Signature, error) {
	if err := b.ready(); err != nil {
		return etims.Signature{}, err
	}

	if err := inv.Validate(); err != nil {
		return etims.Signature{}, err
	}

	req := b.salesRequest(inv)

	var data struct {
		CurRcptNo   json.Number `json:"curRcptNo"`
		TotRcptNo   json.Number `json:"totRcptNo"`
		IntrlData   string      `json:"intrlData"`
		RcptSign    string      `json:"rcptSign"`
		SdcDateTime string      `json:"sdcDateTime"`
	}

	if _, err := b.c.call(ctx, "submit_invoice", b.dev.Branch, &b.dev, pathSaveSales, req, &data); err != nil {
		return etims.Signature{}, err
	}

	if data.RcptSign == "" || data.IntrlData == "" {
		return etims.Signature{}, fmt.Errorf("%w: KRA answered 000 without a receipt signature", etims.ErrUnavailable)
	}

	cur, _ := strconv.ParseInt(strings.TrimSpace(data.CurRcptNo.String()), 10, 64)
	tot, _ := strconv.ParseInt(strings.TrimSpace(data.TotRcptNo.String()), 10, 64)

	signedAt, err := time.ParseInLocation(stampLayout, data.SdcDateTime, b.c.loc)
	if err != nil {
		return etims.Signature{}, fmt.Errorf("%w: unreadable sdcDateTime %q", etims.ErrUnavailable, data.SdcDateTime)
	}

	return etims.Signature{ReceiptNo: cur, TotalReceiptNo: tot, InternalData: data.IntrlData, ReceiptSign: data.RcptSign, SignedAt: signedAt}, nil
}

// salesRequest maps an Invoice onto the saveTrnsSalesOsdc body.
func (b *BranchClient) salesRequest(inv *etims.Invoice) map[string]any {
	totals := inv.Totals()

	txType := inv.TransactionType
	if txType == "" {
		txType = etims.TransactionNormal
	}

	status := inv.Status
	if status == "" {
		status = etims.SalesApproved
	}

	salesDate := inv.SalesDate
	if salesDate.IsZero() {
		salesDate = inv.ConfirmedAt
	}

	published := inv.Receipt.PublishedAt
	if published.IsZero() {
		published = inv.ConfirmedAt
	}

	req := map[string]any{
		"tin":          b.dev.Branch.PIN,
		"bhfId":        b.dev.Branch.BranchID,
		"invcNo":       inv.InvoiceNo,
		"orgInvcNo":    inv.OriginalInvoiceNo,
		"trdInvcNo":    inv.TraderInvoiceNo,
		"custTin":      nil,
		"custNm":       nil,
		"salesTyCd":    string(txType),
		"rcptTyCd":     string(inv.ReceiptType),
		"pmtTyCd":      string(inv.PaymentMethod),
		"salesSttsCd":  string(status),
		"cfmDt":        b.stamp(inv.ConfirmedAt),
		"salesDt":      salesDate.In(b.c.loc).Format(dateLayout),
		"stockRlsDt":   nil,
		"cnclReqDt":    nil,
		"cnclDt":       nil,
		"rfdDt":        nil,
		"rfdRsnCd":     nil,
		"totItemCnt":   totals.ItemCount,
		"totTaxblAmt":  money(totals.TaxableCents),
		"totTaxAmt":    money(totals.TaxCents),
		"totAmt":       money(totals.TotalCents),
		"prchrAcptcYn": "N",
		"remark":       nullable(inv.Remark),
		"regrId":       b.Registrar,
		"regrNm":       b.Registrar,
		"modrId":       b.Registrar,
		"modrNm":       b.Registrar,
	}

	for _, t := range etims.AllTaxTypes {
		bucket := totals.ByTaxType[t]
		req["taxblAmt"+string(t)] = money(bucket.TaxableCents)
		req["taxAmt"+string(t)] = money(bucket.TaxCents)
		req["taxRt"+string(t)] = rate(bucket.RateBps)
	}

	if inv.Buyer != nil {
		req["custTin"] = nullable(inv.Buyer.PIN)
		req["custNm"] = nullable(inv.Buyer.Name)
	}

	if inv.ReceiptType == etims.ReceiptCreditNote {
		refunded := inv.RefundedAt
		if refunded.IsZero() {
			refunded = inv.ConfirmedAt
		}

		req["rfdDt"] = b.stamp(refunded)
		req["rfdRsnCd"] = string(inv.CreditNoteReason)
	}

	receipt := map[string]any{
		"custTin":      nil,
		"custMblNo":    nil,
		"rcptPbctDt":   b.stamp(published),
		"trdeNm":       nullable(inv.Receipt.TradeName),
		"adrs":         nullable(inv.Receipt.Address),
		"topMsg":       nullable(inv.Receipt.TopMessage),
		"btmMsg":       nullable(inv.Receipt.BottomMessage),
		"prchrAcptcYn": "N",
	}
	if inv.Buyer != nil {
		receipt["custTin"] = nullable(inv.Buyer.PIN)
		receipt["custMblNo"] = nullable(inv.Buyer.Phone)
	}

	req["receipt"] = receipt

	lines := make([]map[string]any, 0, len(inv.Lines))

	for i := range inv.Lines {
		l := &inv.Lines[i]
		lt := l.Totals(inv.Rates)

		packages := l.Packages
		if packages == 0 {
			packages = l.Quantity
		}

		lines = append(lines, map[string]any{
			"itemSeq":   i + 1,
			"itemCd":    l.ItemCode,
			"itemClsCd": l.ClassCode,
			"itemNm":    l.Name,
			"bcd":       nullable(l.Barcode),
			"pkgUnitCd": l.PackagingUnit,
			"pkg":       qty(packages),
			"qtyUnitCd": l.QuantityUnit,
			"qty":       qty(l.Quantity),
			"prc":       money(l.UnitPriceCents),
			"splyAmt":   money(lt.SupplyCents),
			"dcRt":      rate(lt.DiscountRateBps),
			"dcAmt":     money(l.DiscountCents),
			"isrccCd":   nil,
			"isrccNm":   nil,
			"isrcRt":    nil,
			"isrcAmt":   nil,
			"taxTyCd":   string(l.TaxType),
			"taxblAmt":  money(lt.TaxableCents),
			"taxAmt":    money(lt.TaxCents),
			"totAmt":    money(lt.TotalCents),
		})
	}

	req["itemList"] = lines

	return req
}

// ItemClasses returns KRA's item classification list changed since the
// given time (zero means everything).
func (b *BranchClient) ItemClasses(ctx context.Context, since time.Time) ([]etims.ItemClass, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}

	var data struct {
		List []struct {
			Cd    string      `json:"itemClsCd"`
			Nm    string      `json:"itemClsNm"`
			Lvl   json.Number `json:"itemClsLvl"`
			TaxTy string      `json:"taxTyCd"`
			UseYn string      `json:"useYn"`
		} `json:"itemClsList"`
	}

	code, err := b.c.call(ctx, "item_classes", b.dev.Branch, &b.dev, pathItemClasses, b.sinceRequest(since), &data)
	if err != nil {
		if code == resultNoResults {
			return nil, nil
		}

		return nil, err
	}

	out := make([]etims.ItemClass, 0, len(data.List))
	for _, r := range data.List {
		lvl, _ := strconv.Atoi(r.Lvl.String())
		out = append(out, etims.ItemClass{Code: r.Cd, Name: r.Nm, Level: lvl, TaxType: etims.TaxType(r.TaxTy), Active: r.UseYn == "Y"})
	}

	return out, nil
}

// Codes returns KRA's code lists changed since the given time, flattened to
// one entry per code.
func (b *BranchClient) Codes(ctx context.Context, since time.Time) ([]etims.Code, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}

	var data struct {
		ClsList []struct {
			CdCls   string `json:"cdCls"`
			CdClsNm string `json:"cdClsNm"`
			DtlList []struct {
				Cd         string      `json:"cd"`
				CdNm       string      `json:"cdNm"`
				CdDesc     string      `json:"cdDesc"`
				SrtOrd     json.Number `json:"srtOrd"`
				UserDfnCd1 string      `json:"userDfnCd1"`
				UseYn      string      `json:"useYn"`
			} `json:"dtlList"`
		} `json:"clsList"`
	}

	code, err := b.c.call(ctx, "codes", b.dev.Branch, &b.dev, pathCodes, b.sinceRequest(since), &data)
	if err != nil {
		if code == resultNoResults {
			return nil, nil
		}

		return nil, err
	}

	var out []etims.Code

	for _, cls := range data.ClsList {
		for _, d := range cls.DtlList {
			ord, _ := strconv.Atoi(d.SrtOrd.String())
			out = append(out, etims.Code{
				Class: cls.CdCls, ClassName: cls.CdClsNm, Code: d.Cd, Name: d.CdNm, Description: d.CdDesc,
				SortOrder: ord, UserDefined1: d.UserDfnCd1, Active: d.UseYn == "Y",
			})
		}
	}

	return out, nil
}

// Branches returns the branches registered on the PIN.
func (b *BranchClient) Branches(ctx context.Context, since time.Time) ([]etims.BranchInfo, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}

	var data struct {
		List []struct {
			TIN       string `json:"tin"`
			BhfID     string `json:"bhfId"`
			BhfNm     string `json:"bhfNm"`
			BhfSttsCd string `json:"bhfSttsCd"`
			PrvncNm   string `json:"prvncNm"`
			DstrtNm   string `json:"dstrtNm"`
			SctrNm    string `json:"sctrNm"`
			LocDesc   string `json:"locDesc"`
			MgrNm     string `json:"mgrNm"`
			HqYn      string `json:"hqYn"`
		} `json:"bhfList"`
	}

	code, err := b.c.call(ctx, "branches", b.dev.Branch, &b.dev, pathBranches, b.sinceRequest(since), &data)
	if err != nil {
		if code == resultNoResults {
			return nil, nil
		}

		return nil, err
	}

	out := make([]etims.BranchInfo, 0, len(data.List))
	for i := range data.List {
		r := &data.List[i]
		out = append(out, etims.BranchInfo{
			Branch: etims.Branch{PIN: r.TIN, BranchID: r.BhfID}, Name: r.BhfNm, StatusCode: r.BhfSttsCd,
			County: r.PrvncNm, SubCounty: r.DstrtNm, Locality: r.SctrNm, Location: r.LocDesc, Manager: r.MgrNm, HeadOffice: r.HqYn == "Y",
		})
	}

	return out, nil
}

func (b *BranchClient) sinceRequest(since time.Time) map[string]any {
	if since.IsZero() {
		since = time.Date(2018, 1, 1, 0, 0, 0, 0, b.c.loc)
	}

	return map[string]any{headerTIN: b.dev.Branch.PIN, headerBranchID: b.dev.Branch.BranchID, "lastReqDt": b.stamp(since)}
}

func (b *BranchClient) stamp(t time.Time) string {
	return t.In(b.c.loc).Format(stampLayout)
}

type envelope struct {
	ResultCd  string          `json:"resultCd"`
	ResultMsg string          `json:"resultMsg"`
	ResultDt  string          `json:"resultDt"`
	Data      json.RawMessage `json:"data"`
}

// call posts body to path, decodes the envelope, maps the result code and,
// on success, decodes data into out (when non-nil). It returns the result
// code alongside the error so list calls can treat "001" (no results) as
// empty.
func (c *Client) call(ctx context.Context, op string, branch etims.Branch, dev *etims.Device, path string, body, out any) (string, error) {
	started := time.Now()
	code, err := c.do(ctx, dev, path, body, out)
	c.observer.Observe(&etims.Event{Operation: op, Branch: branch, ResultCode: code, Err: err, Latency: time.Since(started)})

	return code, err
}

func (c *Client) do(ctx context.Context, dev *etims.Device, path string, body, out any) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("%w: encode request: %w", etims.ErrUnavailable, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("%w: build request: %w", etims.ErrUnavailable, err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if dev != nil {
		req.Header.Set(headerTIN, dev.Branch.PIN)
		req.Header.Set(headerBranchID, dev.Branch.BranchID)
		req.Header.Set(headerCmcKey, dev.CommunicationKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", etims.ErrUnavailable, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", fmt.Errorf("%w: read response: %w", etims.ErrUnavailable, err)
	}

	if resp.StatusCode >= http.StatusInternalServerError {
		return "", fmt.Errorf("%w: %s answered HTTP %d: %s", etims.ErrUnavailable, path, resp.StatusCode, detail(raw))
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ResultCd == "" {
		return "", fmt.Errorf("%w: %s answered HTTP %d with an unrecognized body: %s", etims.ErrUnavailable, path, resp.StatusCode, detail(raw))
	}

	if env.ResultCd != resultOK {
		return env.ResultCd, mapResult(env.ResultCd, env.ResultMsg)
	}

	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return env.ResultCd, fmt.Errorf("%w: %s data does not parse: %w", etims.ErrUnavailable, path, err)
		}
	}

	return env.ResultCd, nil
}

// mapResult turns a non-000 result code into the etims error contract
// (specification §4.18). Codes are compared as strings; they are not
// integers and must never be parsed as such.
func mapResult(code, msg string) error {
	kind := etims.ErrRejected

	switch {
	case code == resultDuplicate:
		kind = etims.ErrDuplicate
	case code == resultNoResults:
		// "There is no search result": a list call with nothing new, or a
		// lookup that found nothing. Not a failure of the request.
		kind = etims.ErrRejected
	case strings.HasPrefix(code, "89"), code == "999":
		// 891–899 are the client's own transport and request-building errors
		// and 999 is KRA's unknown error: nothing about the invoice itself.
		kind = etims.ErrUnavailable
	}

	return &etims.ResultError{Code: code, Message: msg, Kind: kind}
}

// money renders cents as a two-place decimal JSON number (10500 cents ->
// 105.00), which is what KRA's NUMBER(18,2) fields expect.
func money(cents int64) json.Number {
	neg := ""
	if cents < 0 {
		neg, cents = "-", -cents
	}

	return json.Number(fmt.Sprintf("%s%d.%02d", neg, cents/100, cents%100))
}

// qty renders hundredths as a two-place decimal JSON number.
func qty(q etims.Qty) json.Number { return json.Number(q.String()) }

// rate renders basis points as a percentage with two places (1600 -> 16.00).
func rate(bps int64) json.Number { return money(bps) }

func nullable(s string) any {
	if s == "" {
		return nil
	}

	return s
}

func detail(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > maxDetailBody {
		return s[:maxDetailBody] + "…"
	}

	return s
}

// IsRetryable reports whether err is one the caller should retry later
// (ErrUnavailable) rather than park for a human.
func IsRetryable(err error) bool { return errors.Is(err, etims.ErrUnavailable) }
