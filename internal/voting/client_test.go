package voting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestMastersClientUsesOfficialWireContract(t *testing.T) {
	var loginCalls atomic.Int32
	var checkCalls atomic.Int32
	var postCalls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.URL.Path == "/ai2026_student/api/login":
			loginCalls.Add(1)
			if request.Method != http.MethodPost {
				t.Errorf("login method=%s", request.Method)
			}
			body, _ := io.ReadAll(request.Body)
			form, _ := url.ParseQuery(string(body))
			if form.Get("login_id") != "student" || form.Get("password") != "password" {
				t.Errorf("unexpected login form")
			}
			return jsonResponse(http.StatusOK, `{"status":"OK","data":{"access_token":"secret-token"}}`), nil
		case request.URL.Path == "/ai2026_student/api/bet" && request.Method == http.MethodGet:
			checkCalls.Add(1)
			if request.URL.Query().Get("race_id[0]") != "202601010101" {
				t.Errorf("unexpected query: %s", request.URL.RawQuery)
			}
			if request.Header.Get("Authorization") != "Bearer secret-token" {
				t.Error("missing bearer token")
			}
			return jsonResponse(http.StatusOK, `{"status":"OK","data":[]}`), nil
		case request.URL.Path == "/ai2026_student/api/bet" && request.Method == http.MethodPost:
			postCalls.Add(1)
			var body struct {
				BetData []struct {
					RaceID        string          `json:"race_id"`
					Mark          map[string]int  `json:"mark"`
					Bet           []Bet           `json:"bet"`
					LegacyBetList json.RawMessage `json:"bet_list"`
				} `json:"bet_data"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.BetData) != 1 || body.BetData[0].RaceID != "202601010101" ||
				len(body.BetData[0].Bet) != 1 || body.BetData[0].Bet[0].BetID != "b3_c0_2_5" {
				t.Errorf("unexpected POST body: %+v", body)
			}
			if len(body.BetData[0].LegacyBetList) != 0 {
				t.Errorf("POST must use bet, not bet_list: %s", body.BetData[0].LegacyBetList)
			}
			return jsonResponse(http.StatusOK, `{"status":"OK","data":{"success_count":1,"error_count":0},"remaining_money":"999900"}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})

	policy := testPolicy(t)
	client, err := NewMastersClient("http://127.0.0.1", policy)
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = transport
	token, err := client.Login(context.Background(), "student", "password")
	if err != nil || token != "secret-token" {
		t.Fatalf("login token=%q err=%v", token, err)
	}
	votes, err := client.Check(context.Background(), token, "202601010101")
	if err != nil || len(votes) != 0 {
		t.Fatalf("check votes=%v err=%v", votes, err)
	}
	receipt, err := client.Submit(context.Background(), token, minimalPayload())
	if err != nil || receipt.SuccessCount != 1 || receipt.ErrorCount != 0 || receipt.RemainingMoney != "999900" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if loginCalls.Load() != 1 || checkCalls.Load() != 1 || postCalls.Load() != 1 {
		t.Fatal("unexpected request counts")
	}
}

func TestMastersClientPOSTFailureIsAmbiguousAndContainsNoSecret(t *testing.T) {
	client, err := NewMastersClient("http://127.0.0.1", testPolicy(t))
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusBadGateway, `{"status":"NG"}`), nil
	})
	_, err = client.Submit(context.Background(), "do-not-log", minimalPayload())
	var apiError *APIError
	if !errorsAs(err, &apiError) || !apiError.Ambiguous {
		t.Fatalf("expected ambiguous APIError, got %v", err)
	}
	if strings.Contains(err.Error(), "do-not-log") {
		t.Fatal("error leaked bearer token")
	}
}

func TestMastersClientReportsBoundedAPIReason(t *testing.T) {
	client, err := NewMastersClient("http://127.0.0.1", testPolicy(t))
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"status":"NG","reason":"  no vote data\nfor race  "}`), nil
	})
	_, err = client.Check(context.Background(), "secret-token", "202601010101")
	var apiError *APIError
	if !errorsAs(err, &apiError) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if apiError.APIReason != "no vote data for race" {
		t.Fatalf("reason=%q", apiError.APIReason)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatal("error leaked bearer token")
	}
}

func TestEmptyPrecheckResponseClassificationIsNarrow(t *testing.T) {
	empty := &APIError{
		Operation: "check_bet", HTTPStatus: http.StatusOK, APIStatus: "NG",
	}
	if !IsEmptyPrecheckResponse(empty) {
		t.Fatal("expected empty precheck response")
	}
	withReason := *empty
	withReason.APIReason = "invalid race"
	if IsEmptyPrecheckResponse(&withReason) {
		t.Fatal("reason-bearing NG must remain fail-closed")
	}
	wrongOperation := *empty
	wrongOperation.Operation = "submit_bet"
	if IsEmptyPrecheckResponse(&wrongOperation) {
		t.Fatal("submit NG must never be classified as empty precheck")
	}
}

func TestDecodeCheckedVotesAcceptsAsyncEmptyObjectAndSingleVote(t *testing.T) {
	votes, err := decodeCheckedVotes(json.RawMessage(`{}`))
	if err != nil || len(votes) != 0 {
		t.Fatalf("empty object votes=%v err=%v", votes, err)
	}
	votes, err = decodeCheckedVotes(json.RawMessage(`{"race_id":"202607030105","mark":{"10":1},"bet":[{"bet_id":"b1_c0_10","money":"100"}]}`))
	if err != nil || len(votes) != 1 || votes[0].RaceID != "202607030105" || len(votes[0].Bet) != 1 {
		t.Fatalf("single object votes=%v err=%v", votes, err)
	}
	if _, err := decodeCheckedVotes(json.RawMessage(`{"unexpected":[]}`)); err == nil {
		t.Fatal("unknown object shape must remain fail-closed")
	}
	votes, err = decodeCheckedVotes(json.RawMessage(`[{"race_id":"202607030105","mark":{"10":"1"},"bet":[{"bet_id":"b1_c0_10","money":100}]}]`))
	if err != nil || len(votes) != 1 || votes[0].Mark["10"] != 1 || votes[0].Bet[0].Money != "100" {
		t.Fatalf("mixed wire types votes=%v err=%v", votes, err)
	}
}

func TestSubmitCapturesListErrorReason(t *testing.T) {
	policy := testPolicy(t)
	client, err := NewMastersClient("https://example.test", policy)
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"status":"OK","data":{"success_count":0,"error_count":1,"list_error":["受付時間外"]}}`), nil
	})
	_, err = client.Submit(context.Background(), "token", RacePayload{
		RaceID:  "202604020805",
		Mark:    map[string]int{"1": 1},
		BetList: []Bet{{BetID: "b3_c0_1_2", Money: "100"}},
	})
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if apiError.APIReason != `["受付時間外"]` {
		t.Fatalf("unexpected reason: %q", apiError.APIReason)
	}
}

// Kept as a small wrapper so tests do not accidentally print a full error
// chain containing request internals if this assertion fails.
func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
