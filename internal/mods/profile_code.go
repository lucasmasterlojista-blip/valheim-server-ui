package mods

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jonasthim/valheim-server-ui/internal/domain"
)

const (
	thunderstoreProfileURL     = "https://thunderstore.io/api/experimental/legacyprofile/get/"
	thunderstoreProfileTimeout = 15 * time.Second
	thunderstoreProfileMarker  = "#r2modman"
)

func validateProfileCode(message string) error {
	return domain.Validation([]domain.FieldError{{Field: "code", Message: message}})
}

// CompareProfileCode reads a shared client profile from Thunderstore and
// compares its manifest with this instance's installed mods.
func (s *Service) CompareProfileCode(ctx context.Context, instanceID, code string) (*domain.ModProfileComparison, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	key, err := uuid.Parse(code)
	if err != nil || key.String() != code {
		return nil, validateProfileCode("enter a valid Thunderstore share code")
	}
	inst, err := s.inst.Get(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	registry, ok := s.regs.Lookup(domain.RegistryThunderstoreID)
	if !ok {
		return nil, domain.E(domain.CodeUpstreamError, "Thunderstore is unavailable")
	}
	data, err := registry.downloadProfileCode(ctx, code)
	if err != nil {
		return nil, err
	}
	profile, err := parseProfileManifest(data)
	if err != nil {
		return nil, profileCodeField(err)
	}
	result, err := s.compareProfileManifest(ctx, instanceID, inst, profile)
	return result, profileCodeField(err)
}

func profileCodeField(err error) error {
	if err == nil {
		return nil
	}
	if profileErr := domain.AsError(err); profileErr.Code == domain.CodeValidationFailed && len(profileErr.Fields) == 1 && profileErr.Fields[0].Field == "file" {
		return validateProfileCode(profileErr.Fields[0].Message)
	}
	return err
}

func (t *Thunderstore) downloadProfileCode(ctx context.Context, code string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, thunderstoreProfileTimeout)
	defer cancel()
	// r2modman resolves a share code through this endpoint. Thunderstore then
	// redirects to storage; the bytes are a marker followed by base64 ZIP data.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, thunderstoreProfileURL+code+"/", nil)
	if err != nil {
		return nil, domain.Wrap(domain.CodeUpstreamError, "could not request Thunderstore profile", err)
	}
	req.Header.Set("User-Agent", t.userAgent)
	client := *t.http
	client.Jar = nil
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		u := req.URL
		if len(via) != 1 || u.Scheme != "https" || u.User != nil {
			return http.ErrUseLastResponse
		}
		switch strings.ToLower(u.Host) {
		case "ccdn.thunderstore.io", "gcdn.thunderstore.io", "hcdn-1.hcdn.thunderstore.io":
			storagePath := strings.TrimSuffix(u.EscapedPath(), "/")
			if storagePath == "/live/modpacks/legacyprofile/"+code || storagePath == "/modpacks/legacyprofile/"+code {
				return nil
			}
		}
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, domain.Wrap(domain.CodeUpstreamError, "could not reach Thunderstore for this profile", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, validateProfileCode("Thunderstore share code was not found or has expired")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, domain.Ef(domain.CodeUpstreamError, "Thunderstore profile request failed (HTTP %d)", resp.StatusCode)
	}
	return decodeProfileCode(resp.Body)
}

func decodeProfileCode(body io.Reader) ([]byte, error) {
	// Allow the canonical marker, CRLF and a final line ending alongside the
	// encoded archive while enforcing the same decoded limit as file uploads.
	maxBytes := base64.StdEncoding.EncodedLen(maxModProfileBytes) + len(thunderstoreProfileMarker) + 4
	data, err := io.ReadAll(io.LimitReader(body, int64(maxBytes)+1))
	if err != nil {
		return nil, domain.Wrap(domain.CodeUpstreamError, "could not read Thunderstore profile", err)
	}
	tooLarge := "mod profile ZIP must be 8 MiB or smaller"
	if len(data) > maxBytes {
		return nil, validateProfileCode(tooLarge)
	}
	marker, encoded, ok := bytes.Cut(data, []byte{'\n'})
	if !ok || string(bytes.TrimSuffix(marker, []byte{'\r'})) != thunderstoreProfileMarker {
		return nil, validateProfileCode("Thunderstore returned an unsupported profile format")
	}
	encoded = bytes.TrimSpace(encoded)
	archive := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(archive, encoded)
	if err != nil {
		return nil, validateProfileCode("Thunderstore returned invalid profile data")
	}
	if n > maxModProfileBytes {
		return nil, validateProfileCode(tooLarge)
	}
	return archive[:n], nil
}
