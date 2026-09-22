package voting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const OfficialAPIBaseURL = "https://masters.netkeiba.com"

type API interface {
	Login(context.Context, string, string) (string, error)
	Check(context.Context, string, string) ([]CheckedVote, error)
	Submit(context.Context, string, RacePayload) (SubmitReceipt, error)
}

type APIError struct {
	Operation  string
	Message    string
	HTTPStatus int
	APIStatus  string
	APIReason  string
	Ambiguous  bool
}

func (err *APIError) Error() string {
	parts := []string{"operation=" + err.Operation, err.Message}
	if err.HTTPStatus != 0 {
		parts = append(parts, "http_status="+strconv.Itoa(err.HTTPStatus))
	}
	if err.APIStatus != "" {
		parts = append(parts, "api_status="+err.APIStatus)
	}
	if err.APIReason != "" {
		parts = append(parts, "api_reason="+err.APIReason)
	}
	if err.Ambiguous {
		parts = append(parts, "ambiguous=true")
	}
	return strings.Join(parts, "; ")
}

type SubmitReceipt struct {
	SuccessCount   int    `json:"success_count"`
	ErrorCount     int    `json:"error_count"`
	RemainingMoney string `json:"remaining_money,omitempty"`
}

type MastersClient struct {
	baseURL         *url.URL
	client          *http.Client
	maxResponseBody int64
}

func NewMastersClient(base string, policy Policy) (*MastersClient, error) {
	baseURL, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, errors.New("invalid Masters API base URL")
	}
	if baseURL.Scheme != "https" {
		host := baseURL.Hostname()
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("non-HTTPS Masters API URL is permitted only on loopback")
		}
	}
	dialer := &net.Dialer{Timeout: time.Duration(policy.HTTP.ConnectTimeoutSeconds) * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   time.Duration(policy.HTTP.TLSHandshakeTimeoutSeconds) * time.Second,
		ResponseHeaderTimeout: time.Duration(policy.HTTP.ResponseHeaderTimeoutSeconds) * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
	}
	return &MastersClient{
		baseURL: baseURL,
		client: &http.Client{
			Transport: transport,
			// Each operation also applies a context deadline. A client timeout is
			// retained as a final guard against a caller without a deadline.
			Timeout: time.Duration(policy.HTTP.POSTRequestTimeoutSeconds) * time.Second,
		},
		maxResponseBody: policy.HTTP.MaxResponseBodyBytes,
	}, nil
}

type apiEnvelope struct {
	Status         string          `json:"status"`
	Data           json.RawMessage `json:"data"`
	RemainingMoney json.RawMessage `json:"remaining_money"`
	Reason         string          `json:"reason"`
}

// submitRacePayload is deliberately separate from RacePayload.  Plan bundles
// keep the internal, hash-stable `bet_list` field, while the Masters 2026 POST
// endpoint expects the historical wire key `bet` (confirmed by the operator
// during the 2026-08-22 live rehearsal).
type submitRacePayload struct {
	RaceID string         `json:"race_id"`
	Mark   map[string]int `json:"mark"`
	Bet    []Bet          `json:"bet"`
}

func safeAPIReason(reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) > 160 {
		reason = reason[:160]
	}
	return reason
}

// IsEmptyPrecheckResponse recognizes the undocumented response returned by
// the distributed API when GET is called before a vote exists. The sample
// client documents GET only as a post-submit confirmation operation. Keep the
// classification deliberately narrow: HTTP 200, check_bet, status NG, and no
// reason. Any reason text or different status remains fail-closed.
func IsEmptyPrecheckResponse(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) &&
		apiError.Operation == "check_bet" &&
		apiError.HTTPStatus == http.StatusOK &&
		apiError.APIStatus == "NG" &&
		apiError.APIReason == "" &&
		!apiError.Ambiguous
}

func (client *MastersClient) Login(ctx context.Context, loginID, password string) (string, error) {
	if strings.TrimSpace(loginID) == "" || strings.TrimSpace(password) == "" {
		return "", errors.New("login credentials are required")
	}
	form := url.Values{"login_id": {loginID}, "password": {password}}
	request, err := client.newRequest(ctx, http.MethodPost, "/ai2026_student/api/login", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	envelope, err := client.do(request, "login", false)
	if err != nil {
		return "", err
	}
	var data struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil || strings.TrimSpace(data.AccessToken) == "" {
		return "", &APIError{Operation: "login", Message: "response omitted access_token"}
	}
	return data.AccessToken, nil
}

func (client *MastersClient) Check(ctx context.Context, token, raceID string) ([]CheckedVote, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("access token is required")
	}
	if !raceIDPattern.MatchString(raceID) {
		return nil, errors.New("race_id must be exactly 12 ASCII letters/digits")
	}
	query := url.Values{"race_id[0]": {raceID}}
	request, err := client.newRequest(ctx, http.MethodGet, "/ai2026_student/api/bet?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	envelope, err := client.do(request, "check_bet", false)
	if err != nil {
		return nil, err
	}
	votes, err := decodeCheckedVotes(envelope.Data)
	if err != nil {
		return nil, &APIError{Operation: "check_bet", Message: err.Error()}
	}
	SortVotes(votes)
	return votes, nil
}

func decodeCheckedVotes(data json.RawMessage) ([]CheckedVote, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("response data was empty")
	}
	if trimmed[0] == '[' {
		var entries []json.RawMessage
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, errors.New("response data must contain valid vote entries")
		}
		votes := make([]CheckedVote, 0, len(entries))
		for _, entry := range entries {
			vote, err := decodeCheckedVote(entry)
			if err != nil {
				return nil, err
			}
			votes = append(votes, vote)
		}
		return votes, nil
	}
	if trimmed[0] != '{' {
		return nil, errors.New("response data must be a vote array or object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, errors.New("response data must contain a valid vote object")
	}
	// The live API returns {} while an accepted asynchronous POST is not yet
	// visible. Treat only the exactly empty object as no vote; arbitrary object
	// shapes remain fail-closed.
	if len(object) == 0 {
		return []CheckedVote{}, nil
	}
	if _, ok := object["race_id"]; !ok {
		return nil, errors.New("response vote object omitted race_id")
	}
	vote, err := decodeCheckedVote(trimmed)
	if err != nil {
		return nil, err
	}
	return []CheckedVote{vote}, nil
}

func decodeCheckedVote(data json.RawMessage) (CheckedVote, error) {
	var wire struct {
		RaceID string                     `json:"race_id"`
		Mark   map[string]json.RawMessage `json:"mark"`
		Bet    []struct {
			BetID string          `json:"bet_id"`
			Money json.RawMessage `json:"money"`
		} `json:"bet"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || !raceIDPattern.MatchString(wire.RaceID) {
		return CheckedVote{}, errors.New("response vote entry has invalid race_id")
	}
	mark := make(map[string]int, len(wire.Mark))
	for horse, raw := range wire.Mark {
		var number int
		if err := json.Unmarshal(raw, &number); err != nil {
			var text string
			if json.Unmarshal(raw, &text) != nil {
				return CheckedVote{}, errors.New("response vote entry has invalid mark")
			}
			parsed, parseErr := strconv.Atoi(text)
			if parseErr != nil {
				return CheckedVote{}, errors.New("response vote entry has invalid mark")
			}
			number = parsed
		}
		mark[horse] = number
	}
	bets := make([]Bet, 0, len(wire.Bet))
	for _, item := range wire.Bet {
		var money string
		if err := json.Unmarshal(item.Money, &money); err != nil {
			var number json.Number
			if json.Unmarshal(item.Money, &number) != nil {
				return CheckedVote{}, errors.New("response vote entry has invalid money")
			}
			money = string(number)
		}
		bets = append(bets, Bet{BetID: item.BetID, Money: money})
	}
	return CheckedVote{RaceID: wire.RaceID, Mark: mark, Bet: bets}, nil
}

func (client *MastersClient) Submit(ctx context.Context, token string, payload RacePayload) (SubmitReceipt, error) {
	if strings.TrimSpace(token) == "" {
		return SubmitReceipt{}, errors.New("access token is required")
	}
	if err := ValidatePayload(payload); err != nil {
		return SubmitReceipt{}, err
	}
	body, err := json.Marshal(struct {
		BetData []submitRacePayload `json:"bet_data"`
	}{BetData: []submitRacePayload{{
		RaceID: payload.RaceID,
		Mark:   payload.Mark,
		Bet:    payload.BetList,
	}}})
	if err != nil {
		return SubmitReceipt{}, err
	}
	request, err := client.newRequest(ctx, http.MethodPost, "/ai2026_student/api/bet", bytes.NewReader(body))
	if err != nil {
		return SubmitReceipt{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	envelope, err := client.do(request, "submit_bet", true)
	if err != nil {
		return SubmitReceipt{}, err
	}
	var raw struct {
		SuccessCount json.Number     `json:"success_count"`
		ErrorCount   json.Number     `json:"error_count"`
		ListError    json.RawMessage `json:"list_error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Data))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return SubmitReceipt{}, &APIError{Operation: "submit_bet", Message: "response omitted success_count/error_count", Ambiguous: true}
	}
	success, successErr := strconv.Atoi(string(raw.SuccessCount))
	failures, failureErr := strconv.Atoi(string(raw.ErrorCount))
	if successErr != nil || failureErr != nil {
		return SubmitReceipt{}, &APIError{Operation: "submit_bet", Message: "response counts were invalid", Ambiguous: true}
	}
	receipt := SubmitReceipt{SuccessCount: success, ErrorCount: failures}
	if len(envelope.RemainingMoney) > 0 && string(envelope.RemainingMoney) != "null" {
		var text string
		if json.Unmarshal(envelope.RemainingMoney, &text) == nil {
			receipt.RemainingMoney = text
		} else {
			receipt.RemainingMoney = strings.Trim(string(envelope.RemainingMoney), `"`)
		}
	}
	if receipt.SuccessCount != 1 || receipt.ErrorCount != 0 {
		reason := ""
		if len(raw.ListError) > 0 && string(raw.ListError) != "null" && string(raw.ListError) != "[]" {
			reason = safeAPIReason(string(raw.ListError))
		}
		return SubmitReceipt{}, &APIError{
			Operation: "submit_bet", Message: "batch was not accepted",
			APIReason: reason,
		}
	}
	return receipt, nil
}

func (client *MastersClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	relative, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	return http.NewRequestWithContext(ctx, method, client.baseURL.ResolveReference(relative).String(), body)
}

func (client *MastersClient) do(request *http.Request, operation string, post bool) (apiEnvelope, error) {
	response, err := client.client.Do(request)
	if err != nil {
		return apiEnvelope{}, &APIError{Operation: operation, Message: "transport failure", Ambiguous: post}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, client.maxResponseBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return apiEnvelope{}, &APIError{Operation: operation, Message: "response body failure", HTTPStatus: response.StatusCode, Ambiguous: post}
	}
	if int64(len(body)) > client.maxResponseBody {
		return apiEnvelope{}, &APIError{Operation: operation, Message: "response exceeded size limit", HTTPStatus: response.StatusCode, Ambiguous: post}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return apiEnvelope{}, &APIError{Operation: operation, Message: "unexpected HTTP response", HTTPStatus: response.StatusCode, Ambiguous: post}
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return apiEnvelope{}, &APIError{Operation: operation, Message: "response was not valid JSON", HTTPStatus: response.StatusCode, Ambiguous: post}
	}
	if envelope.Status != "OK" {
		status := envelope.Status
		if status == "" {
			status = "missing"
		}
		return apiEnvelope{}, &APIError{
			Operation: operation, Message: "API rejected the request",
			HTTPStatus: response.StatusCode, APIStatus: status,
			APIReason: safeAPIReason(envelope.Reason),
		}
	}
	if len(envelope.Data) == 0 {
		return apiEnvelope{}, &APIError{Operation: operation, Message: "successful response omitted data", HTTPStatus: response.StatusCode, Ambiguous: post}
	}
	return envelope, nil
}
