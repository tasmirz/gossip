package coord

import (
	"crypto"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sync"
)

type enrollmentRequest struct {
	Project    string     `json:"project_id"`
	ID         string     `json:"id"`
	Code       string     `json:"code,omitempty"`
	CSR        []byte     `json:"csr,omitempty"`
	Credential Credential `json:"credential,omitempty"`
}

// EnrollmentHandler only adds enrollment routes. The RPC handler continues to
// verify the TLS identity, certificate grant and credential for all RPC calls.
func EnrollmentHandler(s *Store, peer http.Handler, ca *x509.Certificate, signer crypto.Signer) http.Handler {
	var mu sync.Mutex
	window := time.Now()
	requests := 0
	allow := func() bool {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(window) >= time.Minute {
			window = time.Now()
			requests = 0
		}
		if requests >= 60 {
			return false
		}
		requests++
		return true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/enrollment/") {
			peer.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost || r.TLS == nil || r.URL.RawQuery != "" || r.Header.Get("Origin") != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.NotFound(w, r)
			return
		}
		if !allow() {
			http.Error(w, "try again shortly", http.StatusTooManyRequests)
			return
		}
		var input enrollmentRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&input); err != nil || input.Project == "" || input.ID == "" || len(input.Project) > 128 || len(input.ID) > 128 {
			http.Error(w, "invalid enrollment request", http.StatusBadRequest)
			return
		}
		var result any
		var err error
		switch r.URL.Path {
		case "/v1/enrollment/accept":
			result, err = s.AcceptRemoteInvitation(r.Context(), input.Project, input.ID, input.Code, input.CSR)
		case "/v1/enrollment/certificate":
			var cert []byte
			cert, err = s.IssueEnrollmentCertificate(r.Context(), input.Project, input.ID, input.Code, input.CSR, ca, signer)
			result = map[string][]byte{"certificate": cert}
		case "/v1/enrollment/finalize":
			if len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
				http.Error(w, "verified client certificate required", http.StatusUnauthorized)
				return
			}
			err = s.FinalizeRemoteInvitation(r.Context(), input.Project, input.ID, r.TLS.VerifiedChains[0][0], input.Credential)
			result = map[string]bool{"enrolled": err == nil}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "enrollment request rejected", http.StatusForbidden)
			return
		}
		jsonReply(w, result)
	})
}

func validateEnrollmentHome(endpoint, serverCA string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("enter the HTTPS peer endpoint, including its port if needed")
	}
	if len(serverCA) > 32768 || !x509.NewCertPool().AppendCertsFromPEM([]byte(serverCA)) {
		return errors.New("provide the public server CA certificate in PEM format")
	}
	return nil
}
