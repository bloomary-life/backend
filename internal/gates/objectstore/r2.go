package objectstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
)

const (
	defaultR2Region       = "auto"
	defaultPresignTTL     = 10 * time.Minute
	defaultMaxUploadBytes = 5 << 20
	unsignedPayload       = "UNSIGNED-PAYLOAD"
	aws4Algorithm         = "AWS4-HMAC-SHA256"
	aws4Request           = "aws4_request"
)

type R2Config struct {
	AccountID      string
	Endpoint       string
	Bucket         string
	AccessKeyID    string
	SecretKey      string
	Environment    string
	PresignTTL     time.Duration
	MaxUploadBytes int64
}

type R2ObjectStore struct {
	endpoint       *url.URL
	bucket         string
	accessKeyID    string
	secretKey      string
	environment    string
	presignTTL     time.Duration
	maxUploadBytes int64
}

var _ draftDomain.ObjectStore = (*R2ObjectStore)(nil)

func NewR2ObjectStore(config R2Config) (*R2ObjectStore, error) {
	endpoint := strings.TrimSpace(config.Endpoint)
	if endpoint == "" && strings.TrimSpace(config.AccountID) != "" {
		endpoint = "https://" + strings.TrimSpace(config.AccountID) + ".r2.cloudflarestorage.com"
	}
	if endpoint == "" || strings.TrimSpace(config.Bucket) == "" ||
		strings.TrimSpace(config.AccessKeyID) == "" || strings.TrimSpace(config.SecretKey) == "" {
		return nil, fmt.Errorf("r2 object store: endpoint, bucket, access key, and secret key are required")
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse r2 endpoint: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("r2 endpoint must include scheme and host")
	}

	environment := strings.TrimSpace(config.Environment)
	if environment == "" {
		environment = "production"
	}
	presignTTL := config.PresignTTL
	if presignTTL <= 0 {
		presignTTL = defaultPresignTTL
	}
	maxBytes := config.MaxUploadBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxUploadBytes
	}

	return &R2ObjectStore{
		endpoint:       parsed,
		bucket:         strings.Trim(strings.TrimSpace(config.Bucket), "/"),
		accessKeyID:    strings.TrimSpace(config.AccessKeyID),
		secretKey:      config.SecretKey,
		environment:    pathPart(environment),
		presignTTL:     presignTTL,
		maxUploadBytes: maxBytes,
	}, nil
}

func (s *R2ObjectStore) StagingObjectKey(memberID, draftID, imageID string) string {
	return s.imageObjectKey("staging", memberID, draftID, imageID)
}

func (s *R2ObjectStore) LinkedObjectKey(memberID, draftID, imageID string) string {
	return s.imageObjectKey("linked", memberID, draftID, imageID)
}

func (s *R2ObjectStore) Stat(ctx context.Context, objectKey string) (draftDomain.ObjectMetadata, error) {
	req, err := s.signedRequest(ctx, http.MethodHead, objectKey, nil)
	if err != nil {
		return draftDomain.ObjectMetadata{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return draftDomain.ObjectMetadata{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return draftDomain.ObjectMetadata{}, fmt.Errorf("r2 head object status=%d", resp.StatusCode)
	}

	metadata := map[string]string{}
	for key, values := range resp.Header {
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, "x-amz-meta-") || len(values) == 0 {
			continue
		}
		metadata[strings.TrimPrefix(lower, "x-amz-meta-")] = strings.TrimSpace(values[0])
	}

	return draftDomain.ObjectMetadata{
		ObjectKey:   strings.TrimLeft(strings.TrimSpace(objectKey), "/"),
		ContentType: strings.TrimSpace(resp.Header.Get("Content-Type")),
		SizeBytes:   resp.ContentLength,
		Metadata:    metadata,
	}, nil
}

func (s *R2ObjectStore) Copy(ctx context.Context, sourceKey, destinationKey string) error {
	headers := map[string]string{
		"x-amz-copy-source":        "/" + s.bucket + "/" + strings.TrimLeft(strings.TrimSpace(sourceKey), "/"),
		"x-amz-metadata-directive": "COPY",
	}
	req, err := s.signedRequest(ctx, http.MethodPut, destinationKey, headers)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("r2 copy object status=%d", resp.StatusCode)
	}

	return nil
}

func (s *R2ObjectStore) Delete(ctx context.Context, objectKey string) error {
	req, err := s.signedRequest(ctx, http.MethodDelete, objectKey, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("r2 delete object status=%d", resp.StatusCode)
	}

	return nil
}

func (s *R2ObjectStore) Read(ctx context.Context, objectKey string) ([]byte, error) {
	req, err := s.signedRequest(ctx, http.MethodGet, objectKey, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("r2 read object status=%d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (s *R2ObjectStore) PresignPut(
	_ context.Context, input draftDomain.PresignPutInput,
) (string, time.Time, error) {
	return s.presign(httpMethodPut, input.ObjectKey, input.Headers)
}

func (s *R2ObjectStore) PresignGet(_ context.Context, objectKey string) (string, time.Time, error) {
	return s.presign(http.MethodGet, objectKey, nil)
}

func (s *R2ObjectStore) MaxUploadBytes() int64 {
	return s.maxUploadBytes
}

func (s *R2ObjectStore) imageObjectKey(state, memberID, draftID, imageID string) string {
	return strings.Join(
		[]string{
			"draft-images",
			s.environment,
			state,
			pathPart(memberID),
			pathPart(draftID),
			pathPart(imageID),
		},
		"/",
	)
}

func (s *R2ObjectStore) signedRequest(
	ctx context.Context, method, objectKey string, headers map[string]string,
) (*http.Request, error) {
	objectKey = strings.TrimLeft(strings.TrimSpace(objectKey), "/")
	if objectKey == "" {
		return nil, fmt.Errorf("object key is required")
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	scope := date + "/" + defaultR2Region + "/s3/" + aws4Request
	signed := map[string]string{
		"x-amz-content-sha256": unsignedPayload,
		"x-amz-date":           amzDate,
	}
	for key, value := range headers {
		signed[key] = value
	}
	signedHeaders, canonicalHeaders := canonicalizeHeaders(s.endpoint.Host, signed)
	canonicalRequest := strings.Join(
		[]string{
			method,
			s.canonicalURI(objectKey),
			"",
			canonicalHeaders,
			signedHeaders,
			unsignedPayload,
		},
		"\n",
	)
	stringToSign := strings.Join(
		[]string{
			aws4Algorithm,
			amzDate,
			scope,
			sha256Hex(canonicalRequest),
		},
		"\n",
	)
	signature := hex.EncodeToString(hmacSHA256(signingKey(s.secretKey, date), stringToSign))

	out := *s.endpoint
	out.Path = s.objectPath(objectKey)
	out.RawPath = s.canonicalURI(objectKey)
	req, err := http.NewRequestWithContext(ctx, method, out.String(), nil)
	if err != nil {
		return nil, err
	}
	for key, value := range signed {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		aws4Algorithm, s.accessKeyID, scope, signedHeaders, signature,
	))
	return req, nil
}

func (s *R2ObjectStore) presign(method, objectKey string, headers map[string]string) (string, time.Time, error) {
	objectKey = strings.TrimLeft(strings.TrimSpace(objectKey), "/")
	if objectKey == "" {
		return "", time.Time{}, fmt.Errorf("object key is required")
	}

	now := time.Now().UTC()
	expiresAt := now.Add(s.presignTTL)
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	scope := date + "/" + defaultR2Region + "/s3/" + aws4Request
	canonicalURI := s.canonicalURI(objectKey)
	signedHeaders, canonicalHeaders := canonicalizeHeaders(s.endpoint.Host, headers)
	query := url.Values{}
	query.Set("X-Amz-Algorithm", aws4Algorithm)
	query.Set("X-Amz-Credential", s.accessKeyID+"/"+scope)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", strconv.Itoa(int(s.presignTTL.Seconds())))
	query.Set("X-Amz-SignedHeaders", signedHeaders)
	canonicalQuery := awsQueryEncode(query)
	canonicalRequest := strings.Join(
		[]string{
			method,
			canonicalURI,
			canonicalQuery,
			canonicalHeaders,
			signedHeaders,
			unsignedPayload,
		},
		"\n",
	)
	stringToSign := strings.Join(
		[]string{
			aws4Algorithm,
			amzDate,
			scope,
			sha256Hex(canonicalRequest),
		},
		"\n",
	)
	query.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA256(signingKey(s.secretKey, date), stringToSign)))

	out := *s.endpoint
	out.Path = s.objectPath(objectKey)
	out.RawPath = canonicalURI
	out.RawQuery = awsQueryEncode(query)
	return out.String(), expiresAt, nil
}

func (s *R2ObjectStore) canonicalURI(objectKey string) string {
	prefix := strings.TrimRight(s.endpoint.EscapedPath(), "/")
	return prefix + "/" + pathEscape(s.bucket) + "/" + pathEscapeObjectKey(objectKey)
}

func (s *R2ObjectStore) objectPath(objectKey string) string {
	prefix := strings.TrimRight(s.endpoint.Path, "/")
	return prefix + "/" + s.bucket + "/" + objectKey
}

func canonicalizeHeaders(host string, headers map[string]string) (string, string) {
	normalized := map[string]string{"host": strings.TrimSpace(host)}
	for key, value := range headers {
		normalized[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}

	keys := make([]string, 0, len(normalized))
	for key := range normalized {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var canonical strings.Builder
	for _, key := range keys {
		canonical.WriteString(key)
		canonical.WriteByte(':')
		canonical.WriteString(normalized[key])
		canonical.WriteByte('\n')
	}

	return strings.Join(keys, ";"), canonical.String()
}

func awsQueryEncode(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		items := append([]string(nil), values[key]...)
		sort.Strings(items)
		for _, value := range items {
			parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
	}

	return strings.ReplaceAll(strings.Join(parts, "&"), "+", "%20")
}

func pathEscapeObjectKey(key string) string {
	parts := strings.Split(key, "/")
	for index, part := range parts {
		parts[index] = pathEscape(part)
	}
	return strings.Join(parts, "/")
}

func pathEscape(value string) string {
	return strings.ReplaceAll(url.PathEscape(value), "+", "%20")
}

func pathPart(value string) string {
	return strings.NewReplacer("/", "_", "\\", "_").Replace(strings.TrimSpace(value))
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func signingKey(secret, date string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+secret), date)
	regionKey := hmacSHA256(dateKey, defaultR2Region)
	serviceKey := hmacSHA256(regionKey, "s3")
	return hmacSHA256(serviceKey, aws4Request)
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

const httpMethodPut = "PUT"
