package apiir

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func server(t *testing.T, replies map[string]string) *Live {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		for path, body := range replies {
			if strings.HasSuffix(r.URL.Path, path) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewLive(srv.URL, "t", "Shahkar", 1, 5*time.Second)
}

// SendSms answers with a number in data (the message id); that is a success, not a decode error.
func TestSendSMSNumericData(t *testing.T) {
	l := server(t, map[string]string{"/SendSms": `{"data": 123456789, "success": true, "code": 0, "message": null}`})
	if err := l.SendSMS(context.Background(), []string{"09120000000"}, "x"); err != nil {
		t.Fatalf("numeric data must be accepted: %v", err)
	}
}

func TestSendSMSRejected(t *testing.T) {
	l := server(t, map[string]string{"/SendSms": `{"data": null, "success": false, "code": "401", "message": "no access"}`})
	if err := l.SendSMS(context.Background(), []string{"09120000000"}, "x"); err == nil {
		t.Fatal("success=false must be an error")
	}
}

func TestShahkarAndOTPBooleans(t *testing.T) {
	l := server(t, map[string]string{
		"/Shahkar": `{"data": true, "success": true, "code": 0}`,
		"/SmsOTP":  `{"data": false, "success": true, "code": 0}`,
	})
	if r := l.Shahkar(context.Background(), "0000000000", "09120000000"); !r.OK || !r.Matched {
		t.Fatalf("shahkar: %+v", r)
	}
	if err := l.SendOTP(context.Background(), "09120000000", "1234"); err == nil {
		t.Fatal("data=false from SmsOTP must be an error")
	}
}
