package storage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// BucketTarget is an external S3-compatible bucket to check.
type BucketTarget struct {
	Endpoint  string
	Region    string
	Bucket    string
	PathStyle bool
	CABundle  string
	AccessKey string
	SecretKey string
}

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var regionPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ValidateBucketTarget checks the non-secret parts of a target and returns
// one message per problem.
func ValidateBucketTarget(target BucketTarget) []string {
	issues := []string{}
	endpoint, err := url.Parse(target.Endpoint)
	switch {
	case target.Endpoint == "":
		issues = append(issues, "endpoint is required")
	case err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Host == "":
		issues = append(issues, "endpoint must be an http(s) URL with a host")
	case endpoint.User != nil:
		issues = append(issues, "endpoint must not embed credentials; use secret_ref")
	case endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/"):
		issues = append(issues, "endpoint must not include a path, query or fragment")
	}
	if !bucketPattern.MatchString(target.Bucket) || strings.Contains(target.Bucket, "..") {
		issues = append(issues, "bucket must be a valid S3 bucket name (3-63 lowercase letters, digits, dots, hyphens)")
	}
	if target.Region != "" && !regionPattern.MatchString(target.Region) {
		issues = append(issues, "region must be lowercase letters, digits and hyphens")
	}
	if !target.PathStyle && strings.Contains(target.Bucket, ".") && endpoint != nil && endpoint.Scheme == "https" {
		issues = append(issues, "buckets containing dots need path_style over https (virtual-hosted names break TLS wildcard certificates)")
	}
	if target.CABundle != "" {
		if _, err := certPool(target.CABundle); err != nil {
			issues = append(issues, err.Error())
		}
	}
	return issues
}

func certPool(bundle string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(bundle)) {
		return nil, errors.New("ca_bundle must contain at least one PEM certificate")
	}
	return pool, nil
}

// HTTPClient returns a client that trusts the target's CA bundle in addition
// to the system roots and never follows redirects (a redirect is reported).
func (target BucketTarget) HTTPClient(timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if target.CABundle != "" {
		pool, err := certPool(target.CABundle)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// CheckBucket performs a SigV4-signed HEAD on the bucket and classifies the
// answer as healthy, degraded or unavailable with an operator-facing reason.
// Credentials never appear in the reason.
func CheckBucket(ctx context.Context, target BucketTarget, client *http.Client) (string, string) {
	if issues := ValidateBucketTarget(target); len(issues) > 0 {
		return "unavailable", strings.Join(issues, "; ")
	}
	if target.AccessKey == "" || target.SecretKey == "" {
		return "configured", "Credentials are not available: set the secret_ref environment variable on the gateway to ACCESS_KEY_ID:SECRET_ACCESS_KEY"
	}
	endpoint, _ := url.Parse(target.Endpoint)
	rawPath := "/" + target.Bucket
	if !target.PathStyle {
		endpoint.Host = target.Bucket + "." + endpoint.Host
		rawPath = "/"
	}
	browser := &Browser{Config: Config{Endpoint: endpoint.Scheme + "://" + endpoint.Host, Region: target.Region, AccessKey: target.AccessKey, SecretKey: target.SecretKey}}
	signed, err := browser.signedURL(http.MethodHead, rawPath, url.Values{}, time.Now())
	if err != nil {
		return "unavailable", err.Error()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, signed, nil)
	if err != nil {
		return "unavailable", err.Error()
	}
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		reason := err.Error()
		if strings.Contains(reason, "certificate") || strings.Contains(reason, "x509") {
			return "unavailable", "TLS verification failed: " + reason + ". Provide the endpoint's CA in ca_bundle."
		}
		return "unavailable", "Endpoint unreachable: " + reason
	}
	defer func() { _ = response.Body.Close() }()
	elapsed := time.Since(started)
	switch status := response.StatusCode; {
	case status == http.StatusOK && elapsed > 3*time.Second:
		return "degraded", fmt.Sprintf("Bucket %s is reachable but answered in %s; expect slow artifact and snapshot transfers", target.Bucket, elapsed.Round(time.Millisecond))
	case status == http.StatusOK:
		return "healthy", fmt.Sprintf("Signed HEAD on bucket %s succeeded in %s", target.Bucket, elapsed.Round(time.Millisecond))
	case status == http.StatusMovedPermanently || status == http.StatusTemporaryRedirect || status == http.StatusBadRequest:
		region := response.Header.Get("X-Amz-Bucket-Region")
		if region != "" && region != target.Region {
			return "degraded", fmt.Sprintf("Bucket %s lives in region %s, not %q; update the region", target.Bucket, region, target.Region)
		}
		return "degraded", fmt.Sprintf("Bucket check returned %s; check region and path_style", response.Status)
	case status == http.StatusForbidden:
		return "unavailable", fmt.Sprintf("Access denied to bucket %s: the credentials are wrong, expired, or lack s3:ListBucket", target.Bucket)
	case status == http.StatusNotFound:
		return "unavailable", fmt.Sprintf("Bucket %s does not exist at this endpoint", target.Bucket)
	case status >= 500:
		return "degraded", fmt.Sprintf("Object store returned %s; it may be overloaded or failing over", response.Status)
	default:
		return "degraded", fmt.Sprintf("Unexpected bucket check response %s", response.Status)
	}
}
