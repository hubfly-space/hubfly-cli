package cli

import (
	"net/http"
	"net/url"
	"strconv"
)

// Object Storage (Dashboard API under /api/v1/projects/{projectId}/storage).
// Request bodies use the Dashboard's camelCase fields; responses carry the
// regional storage service's snake_case shapes, as in openapi.json. GET and
// DELETE routes take their input from the query string.

// storageRegion is a region as GET /api/v1/regions describes it, with the
// product flags `hubfly storage create` needs (types.go's region only knows
// about Cell).
type storageRegion struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	Products struct {
		Cell    bool `json:"cell"`
		Storage bool `json:"storage"`
	} `json:"products"`
}

type storageBucket struct {
	ID                    string `json:"id"`
	TenantID              string `json:"tenant_id"`
	Name                  string `json:"name"`
	Versioning            string `json:"versioning"`
	ImageRetentionSeconds int64  `json:"image_retention_seconds"`
	CreatedAt             string `json:"created_at"`
	DeletedAt             string `json:"deleted_at,omitempty"`
}

type storageOverview struct {
	State       string          `json:"state"`
	QuotaBytes  int64           `json:"quotaBytes"`
	BucketCount int             `json:"bucketCount"`
	Buckets     []storageBucket `json:"buckets"`
	S3Endpoint  string          `json:"s3Endpoint,omitempty"`
}

type storageDayQuantity struct {
	Day      string  `json:"day"`
	Quantity float64 `json:"quantity"`
}

type storageUsageSummary struct {
	TenantID         string               `json:"tenant_id"`
	StorageUsedBytes int64                `json:"storage_used_bytes"`
	QuotaBytes       int64                `json:"quota_bytes"`
	ActiveJobs       int                  `json:"active_jobs"`
	EncodeMinutes    []storageDayQuantity `json:"encode_minutes"`
	EgressBytes      []storageDayQuantity `json:"egress_bytes"`
}

type storageObject struct {
	VersionID    string            `json:"version_id"`
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	Size         int64             `json:"size"`
	ETag         string            `json:"etag"`
	SHA256       string            `json:"sha256"`
	ContentType  string            `json:"content_type"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	DeleteMarker bool              `json:"delete_marker,omitempty"`
	CreatedAt    string            `json:"created_at"`
}

type storageObjectPage struct {
	Objects     []storageObject `json:"objects"`
	Prefixes    []string        `json:"prefixes"`
	NextToken   string          `json:"next_token,omitempty"`
	IsTruncated bool            `json:"is_truncated"`
}

type storageObjectQuery struct {
	Bucket            string
	Prefix            string
	ContinuationToken string
	PageSize          int
	Search            string
	// View "managed" hides generated image variants and HLS output (and is
	// the only view that applies Search); "raw" lists every stored object.
	View string
}

type storageUploadSession struct {
	UploadURL   string `json:"upload_url"`
	ObjectKey   string `json:"object_key"`
	ExpiresAt   string `json:"expires_at"`
	ContentType string `json:"content_type"`
}

type storageDownload struct {
	DownloadURL string `json:"download_url"`
	ExpiresAt   string `json:"expires_at"`
}

type storageDeletedObject struct {
	VersionID    string `json:"version_id"`
	DeleteMarker bool   `json:"delete_marker"`
}

type storageAccessKey struct {
	AccessKey   string `json:"access_key"`
	Name        string `json:"name"`
	Permission  string `json:"permission"`
	BucketScope string `json:"bucket_scope,omitempty"`
	CreatedAt   string `json:"created_at"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
	RevokedAt   string `json:"revoked_at,omitempty"`
}

// storageCredential is a new S3 key pair. SecretKey is returned once.
type storageCredential struct {
	AccessKey   string `json:"access_key"`
	SecretKey   string `json:"secret_key"`
	TenantID    string `json:"tenant_id"`
	Name        string `json:"name"`
	Permission  string `json:"permission"`
	BucketScope string `json:"bucket_scope,omitempty"`
}

type storageRendition struct {
	ID        string `json:"id"`
	AssetID   string `json:"asset_id"`
	Height    int    `json:"height"`
	Width     int    `json:"width"`
	Bandwidth int    `json:"bandwidth"`
	State     string `json:"state"`
	JobID     string `json:"job_id,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// storageAsset is a managed (published) asset. The protected-delivery access
// key is deliberately not part of it: it is handled separately so it never
// lands in --json output by accident.
type storageAsset struct {
	ID              string             `json:"id"`
	URLID           string             `json:"url_id"`
	Bucket          string             `json:"bucket"`
	ObjectKey       string             `json:"object_key"`
	SourceVersionID string             `json:"source_version_id,omitempty"`
	ContentType     string             `json:"content_type"`
	SizeBytes       int64              `json:"size_bytes"`
	FileType        string             `json:"file_type"`
	Title           string             `json:"title"`
	State           string             `json:"state"`
	AccessMode      string             `json:"access_mode"`
	AllowStream     bool               `json:"allow_stream"`
	AllowDownload   bool               `json:"allow_download"`
	AllowPreview    bool               `json:"allow_preview"`
	HLSReady        bool               `json:"hls_ready"`
	DurationSeconds float64            `json:"duration_seconds"`
	CreatedAt       string             `json:"created_at"`
	UpdatedAt       string             `json:"updated_at"`
	Renditions      []storageRendition `json:"renditions"`
	AssetURL        string             `json:"asset_url"`
	PlayerURL       string             `json:"player_url"`
	HasAccessKey    bool               `json:"has_access_key,omitempty"`
}

type storageAssetCreateResult struct {
	Asset     storageAsset `json:"asset"`
	AssetURL  string       `json:"asset_url"`
	PlayerURL string       `json:"player_url"`
	AccessKey string       `json:"access_key"`
}

type storageShare struct {
	Token       string `json:"token"`
	ExpiresAt   string `json:"expires_at"`
	PlayerURL   string `json:"player_url,omitempty"`
	ManifestURL string `json:"manifest_url,omitempty"`
	MediaURL    string `json:"media_url,omitempty"`
	Format      string `json:"format,omitempty"`
}

type storageMediaJob struct {
	ID              string   `json:"id"`
	Bucket          string   `json:"bucket"`
	SourceKey       string   `json:"source_key"`
	MaxHeight       int      `json:"max_height"`
	State           string   `json:"state"`
	Progress        int      `json:"progress"`
	DurationSeconds *float64 `json:"duration_seconds"`
	Error           *string  `json:"error"`
	OutputPrefix    string   `json:"output_prefix"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

type storageMediaJobCreated struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	AssetID string `json:"asset_id,omitempty"`
}

type storageMediaPlayback struct {
	ManifestURL string `json:"manifest_url,omitempty"`
	MediaURL    string `json:"media_url,omitempty"`
	PlayerURL   string `json:"player_url"`
	ExpiresAt   string `json:"expires_at"`
}

func storageAPIPath(projectID string) string {
	return apiHost + "/api/v1/projects/" + url.PathEscape(projectID) + "/storage"
}

// storageSecretRequest runs a request whose response holds a secret (an S3
// secret key, protected asset access key or signed transfer/playback URL). api.go's debug redaction does
// not know these snake_case fields, so debug output shows the request line
// only and never the response body.
func storageSecretRequest(method, requestURL, token string, body, out any) error {
	debugf("HTTP request: %s %s (response holds a secret; body not logged)", method, requestURL)
	previous := debugEnabled
	debugEnabled = false
	defer func() { debugEnabled = previous }()
	return doJSONRequest(method, requestURL, token, body, out)
}

func fetchStorageRegions(token string) ([]storageRegion, error) {
	var payload []storageRegion
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/regions", token, nil, &payload)
	return payload, err
}

func createStorageProject(token, name, regionID, organizationID string) (project, error) {
	body := map[string]any{"name": name, "type": "storage", "regionId": regionID}
	if organizationID != "" {
		body["organizationId"] = organizationID
	}
	var payload project
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/projects/create", token, body, &payload)
	return normalizeProject(payload), err
}

func deleteStorageProject(token, projectID string) error {
	return doJSONRequest(http.MethodDelete, apiHost+"/api/v1/projects/"+url.PathEscape(projectID)+"/delete", token, nil, nil)
}

func fetchStorageOverview(token, projectID string) (storageOverview, error) {
	var payload storageOverview
	err := doJSONRequest(http.MethodGet, storageAPIPath(projectID), token, nil, &payload)
	return payload, err
}

func fetchStorageUsage(token, projectID string) (storageUsageSummary, error) {
	var payload storageUsageSummary
	err := doJSONRequest(http.MethodGet, storageAPIPath(projectID)+"/usage", token, nil, &payload)
	return payload, err
}

func fetchStorageBuckets(token, projectID string) ([]storageBucket, error) {
	var payload []storageBucket
	err := doJSONRequest(http.MethodGet, storageAPIPath(projectID)+"/buckets", token, nil, &payload)
	return payload, err
}

func createStorageBucket(token, projectID, name string) (storageBucket, error) {
	var payload storageBucket
	err := doJSONRequest(http.MethodPost, storageAPIPath(projectID)+"/buckets/create", token, map[string]any{"name": name}, &payload)
	return payload, err
}

// updateStorageBucket sets a bucket's versioning (required by the API) and,
// when retentionSeconds is not nil, its image variant retention.
func updateStorageBucket(token, projectID, name, versioning string, retentionSeconds *int64) (storageBucket, error) {
	body := map[string]any{"name": name, "versioning": versioning}
	if retentionSeconds != nil {
		body["imageRetentionSeconds"] = *retentionSeconds
	}
	var payload storageBucket
	err := doJSONRequest(http.MethodPut, storageAPIPath(projectID)+"/buckets/update", token, body, &payload)
	return payload, err
}

func deleteStorageBucket(token, projectID, name string) error {
	return doJSONRequest(http.MethodDelete, storageAPIPath(projectID)+"/buckets/delete?"+url.Values{"name": {name}}.Encode(), token, nil, nil)
}

func listStorageObjects(token, projectID string, query storageObjectQuery) (storageObjectPage, error) {
	values := url.Values{"name": {query.Bucket}}
	if query.Prefix != "" {
		values.Set("prefix", query.Prefix)
	}
	if query.ContinuationToken != "" {
		values.Set("continuationToken", query.ContinuationToken)
	}
	if query.PageSize > 0 {
		values.Set("pageSize", strconv.Itoa(query.PageSize))
	}
	if query.Search != "" {
		values.Set("search", query.Search)
	}
	if query.View != "" {
		values.Set("view", query.View)
	}
	var payload storageObjectPage
	err := doJSONRequest(http.MethodGet, storageAPIPath(projectID)+"/buckets/objects?"+values.Encode(), token, nil, &payload)
	return payload, err
}

func createStorageUploadSession(token, projectID, bucket, key, contentType string) (storageUploadSession, error) {
	body := map[string]any{"bucket": bucket, "objectKey": key, "contentType": contentType}
	var payload storageUploadSession
	err := storageSecretRequest(http.MethodPost, storageAPIPath(projectID)+"/objects/upload-session", token, body, &payload)
	return payload, err
}

func createStorageDownload(token, projectID, bucket, key string) (storageDownload, error) {
	var payload storageDownload
	query := url.Values{"bucket": {bucket}, "key": {key}}
	err := storageSecretRequest(http.MethodGet, storageAPIPath(projectID)+"/objects/download?"+query.Encode(), token, nil, &payload)
	return payload, err
}

func deleteStorageObject(token, projectID, bucket, key string) (storageDeletedObject, error) {
	var payload storageDeletedObject
	query := url.Values{"bucket": {bucket}, "key": {key}}
	err := doJSONRequest(http.MethodDelete, storageAPIPath(projectID)+"/objects/delete?"+query.Encode(), token, nil, &payload)
	return payload, err
}

func fetchStorageAccessKeys(token, projectID string) ([]storageAccessKey, error) {
	var payload []storageAccessKey
	err := doJSONRequest(http.MethodGet, storageAPIPath(projectID)+"/access-keys", token, nil, &payload)
	return payload, err
}

func createStorageAccessKey(token, projectID, name, permission, bucketScope string) (storageCredential, error) {
	body := map[string]any{"name": name, "permission": permission}
	if bucketScope != "" {
		body["bucketScope"] = bucketScope
	}
	var payload storageCredential
	err := storageSecretRequest(http.MethodPost, storageAPIPath(projectID)+"/access-keys/create", token, body, &payload)
	return payload, err
}

func revokeStorageAccessKey(token, projectID, accessKey string) error {
	return doJSONRequest(http.MethodDelete, storageAPIPath(projectID)+"/access-keys/revoke?"+url.Values{"accessKey": {accessKey}}.Encode(), token, nil, nil)
}

func fetchStorageAssets(token, projectID string) ([]storageAsset, error) {
	var payload []storageAsset
	err := doJSONRequest(http.MethodGet, storageAPIPath(projectID)+"/assets", token, nil, &payload)
	return payload, err
}

// createStorageAsset registers an object as a managed asset. The Dashboard
// always creates it protected and returns its access key once.
func createStorageAsset(token, projectID, bucket, key, title string) (storageAssetCreateResult, error) {
	body := map[string]any{"bucket": bucket, "objectKey": key}
	if title != "" {
		body["title"] = title
	}
	var payload storageAssetCreateResult
	err := storageSecretRequest(http.MethodPost, storageAPIPath(projectID)+"/assets/create", token, body, &payload)
	return payload, err
}

func updateStorageAsset(token, projectID, assetID string, changes map[string]any) (storageAsset, error) {
	body := map[string]any{"assetId": assetID}
	for key, value := range changes {
		body[key] = value
	}
	var payload storageAsset
	err := doJSONRequest(http.MethodPut, storageAPIPath(projectID)+"/assets/update", token, body, &payload)
	return payload, err
}

func deleteStorageAsset(token, projectID, assetID string) error {
	return doJSONRequest(http.MethodDelete, storageAPIPath(projectID)+"/assets/delete?"+url.Values{"assetId": {assetID}}.Encode(), token, nil, nil)
}

func createStorageShare(token, projectID, assetID string, ttlSeconds int) (storageShare, error) {
	body := map[string]any{"assetId": assetID, "ttlSeconds": ttlSeconds}
	var payload storageShare
	err := storageSecretRequest(http.MethodPost, storageAPIPath(projectID)+"/assets/share-token", token, body, &payload)
	return payload, err
}

func createStorageAssetEncoding(token, projectID, assetID string, renditions []int) (storageMediaJobCreated, error) {
	body := map[string]any{"assetId": assetID, "renditions": renditions}
	var payload storageMediaJobCreated
	err := doJSONRequest(http.MethodPost, storageAPIPath(projectID)+"/assets/encode", token, body, &payload)
	return payload, err
}

func fetchStorageMediaJobs(token, projectID string) ([]storageMediaJob, error) {
	var payload []storageMediaJob
	err := doJSONRequest(http.MethodGet, storageAPIPath(projectID)+"/media", token, nil, &payload)
	return payload, err
}

func createStorageMediaJob(token, projectID, bucket, key string, maxHeight int) (storageMediaJobCreated, error) {
	body := map[string]any{"bucket": bucket, "sourceKey": key, "maxHeight": maxHeight}
	var payload storageMediaJobCreated
	err := doJSONRequest(http.MethodPost, storageAPIPath(projectID)+"/media/encode", token, body, &payload)
	return payload, err
}

func createStorageMediaPlayback(token, projectID, jobID string) (storageMediaPlayback, error) {
	var payload storageMediaPlayback
	err := storageSecretRequest(http.MethodPost, storageAPIPath(projectID)+"/media/playback", token, map[string]any{"jobId": jobID}, &payload)
	return payload, err
}
