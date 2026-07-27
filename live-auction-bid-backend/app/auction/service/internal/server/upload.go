package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
	"live-auction-bid/backend/app/auction/service/internal/pkg/idgen"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"
	appsvc "live-auction-bid/backend/app/auction/service/internal/service"
	"live-auction-bid/backend/app/auction/service/internal/storage"
)

const maxImageUploadBytes = 5 << 20

type assetStore interface {
	SaveAssetFile(ctx context.Context, asset data.AssetFile) error
	FindTemporaryAssetForDelete(ctx context.Context, assetID, ownerUserID string) (*data.AssetFile, error)
	MarkAssetDeleted(ctx context.Context, assetID, ownerUserID string) error
}

type uploadHandler struct {
	auth    *auth.Manager
	store   assetStore
	storage storage.StorageProvider
}

type uploadImageResponse struct {
	Result           *v1.ReplyResult  `json:"result"`
	Code             int32            `json:"code"`
	Message          string           `json:"message"`
	RequestID        string           `json:"requestId"`
	TraceID          string           `json:"traceId,omitempty"`
	ServerTimeUnixMs int64            `json:"serverTimeUnixMs"`
	Data             *uploadImageData `json:"data,omitempty"`
}

type uploadImageData struct {
	Asset *uploadedAsset `json:"asset"`
}

type uploadedAsset struct {
	ID              string `json:"id"`
	ImageURL        string `json:"imageUrl"`
	Bucket          string `json:"bucket"`
	ObjectKey       string `json:"objectKey"`
	MimeType        string `json:"mimeType"`
	SizeBytes       int64  `json:"sizeBytes"`
	Status          string `json:"status"`
	ExpiresAtUnixMs int64  `json:"expiresAtUnixMs"`
}

func registerUploadHTTP(srv interface {
	HandleFunc(string, http.HandlerFunc)
}, authManager *auth.Manager, store assetStore, provider storage.StorageProvider) {
	h := &uploadHandler{auth: authManager, store: store, storage: provider}
	srv.HandleFunc("/api/uploads/images", h.handleImageUpload)
	srv.HandleFunc("/api/uploads/images/{asset_id}", h.handleImageDelete)
	srv.HandleFunc("/api/uploads/images/", h.handleImageDelete)
}

func (h *uploadHandler) handleImageUpload(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	requestID := uploadRequestID(r)
	traceID := uploadTraceID(r, requestID)
	if requestctx.RequestID(r.Context()) == "" {
		r = r.WithContext(requestctx.WithRequestContext(r.Context(), requestctx.RequestContext{RequestID: requestID, TraceID: traceID, ServerTimeMs: time.Now().UnixMilli()}))
	}
	w.Header().Set(requestctx.HeaderRequestID, requestID)
	w.Header().Set(requestctx.HeaderTraceID, traceID)
	w.Header().Set(requestctx.HeaderServerTime, fmt.Sprintf("%d", time.Now().UnixMilli()))
	logUploadInfo("upload_image.request", requestID, "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
	if r.Method != http.MethodPost {
		writeUploadError(w, http.StatusMethodNotAllowed, r.Context(), fmt.Errorf("%w: method not allowed", apperr.ErrInvalidArgument), nil)
		return
	}
	ctx, claims, err := h.authenticate(r.Context(), r)
	if err != nil {
		writeUploadError(w, uploadAuthStatus(err), ctx, err, nil)
		return
	}
	r = r.WithContext(ctx)
	if h.storage == nil {
		writeUploadError(w, http.StatusServiceUnavailable, r.Context(), errors.New("image storage provider is not configured"), nil)
		return
	}
	if h.store == nil {
		writeUploadError(w, http.StatusServiceUnavailable, r.Context(), errors.New("asset store is not configured"), nil)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImageUploadBytes+1024)
	if err := r.ParseMultipartForm(maxImageUploadBytes + 1024); err != nil {
		writeUploadError(w, http.StatusBadRequest, r.Context(), fmt.Errorf("%w: invalid multipart image upload", apperr.ErrInvalidArgument), err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeUploadError(w, http.StatusBadRequest, r.Context(), fmt.Errorf("%w: file is required", apperr.ErrInvalidArgument), err)
		return
	}
	defer func() { _ = file.Close() }()
	logUploadInfo("upload_image.file_received", requestID, "file_name", filepath.Base(header.Filename), "declared_size", header.Size, "content_type", header.Header.Get("Content-Type"))

	dataBytes, err := io.ReadAll(io.LimitReader(file, maxImageUploadBytes+1))
	if err != nil {
		writeUploadError(w, http.StatusBadRequest, r.Context(), fmt.Errorf("%w: failed to read upload file", apperr.ErrInvalidArgument), err)
		return
	}
	if len(dataBytes) == 0 {
		writeUploadError(w, http.StatusBadRequest, r.Context(), fmt.Errorf("%w: file is empty", apperr.ErrInvalidArgument), nil)
		return
	}
	if len(dataBytes) > maxImageUploadBytes {
		writeUploadError(w, http.StatusBadRequest, r.Context(), fmt.Errorf("%w: image file must be <= 5MB", apperr.ErrInvalidArgument), nil)
		return
	}

	mimeType, ext, err := validateImageBytes(dataBytes)
	if err != nil {
		writeUploadError(w, http.StatusBadRequest, r.Context(), fmt.Errorf("%w: %s", apperr.ErrInvalidArgument, err.Error()), err)
		return
	}
	assetID := idgen.New("asset")
	bizType := sanitizeBizType(r.FormValue("bizType"))
	if bizType == "" {
		bizType = "lot_image"
	}
	now := time.Now()
	expiresAt := now.Add(24 * time.Hour).UnixMilli()
	objectKey := fmt.Sprintf("temp/%s/%04d/%02d/%s.%s", bizType, now.Year(), int(now.Month()), assetID, ext)
	sum := sha256.Sum256(dataBytes)
	sha := hex.EncodeToString(sum[:])

	storageCtx, cancelStorageUpload := context.WithTimeout(context.WithoutCancel(r.Context()), uploadStorageTimeout())
	defer cancelStorageUpload()
	stored, err := h.storage.PutObject(storageCtx, storage.PutObjectInput{
		ObjectKey:   objectKey,
		Reader:      bytes.NewReader(dataBytes),
		SizeBytes:   int64(len(dataBytes)),
		ContentType: mimeType,
	})
	if err != nil {
		writeUploadError(w, http.StatusBadGateway, r.Context(), errors.New("upload image to object storage failed"), err)
		return
	}
	logUploadInfo("upload_image.storage_put", requestID, "provider", stored.Provider, "bucket", stored.Bucket, "object_key", stored.ObjectKey, "duration_ms", time.Since(startedAt).Milliseconds())
	asset := data.AssetFile{
		ID:              assetID,
		MainAccountID:   auth.EffectiveMainAccountID(claims),
		OwnerUserID:     claims.UserID,
		RoomID:          strings.TrimSpace(r.FormValue("roomId")),
		BizType:         bizType,
		Status:          data.AssetStatusTemporary,
		StorageProvider: stored.Provider,
		Bucket:          stored.Bucket,
		ObjectKey:       stored.ObjectKey,
		PublicURL:       stored.PublicURL,
		OriginalName:    filepath.Base(header.Filename),
		MimeType:        mimeType,
		SizeBytes:       int64(len(dataBytes)),
		SHA256:          sha,
		ExpiresAtUnixMs: expiresAt,
	}
	if err := h.store.SaveAssetFile(storageCtx, asset); err != nil {
		_ = h.storage.DeleteObject(context.Background(), stored.ObjectKey)
		writeUploadError(w, http.StatusInternalServerError, r.Context(), errors.New("save asset file failed"), err)
		return
	}
	responseAsset := &uploadedAsset{ID: asset.ID, ImageURL: asset.PublicURL, Bucket: asset.Bucket, ObjectKey: asset.ObjectKey, MimeType: asset.MimeType, SizeBytes: asset.SizeBytes, Status: asset.Status, ExpiresAtUnixMs: asset.ExpiresAtUnixMs}
	result := appsvc.OKResult(r.Context())
	writeJSON(w, http.StatusOK, uploadImageResponse{
		Result:           result,
		Code:             0,
		Message:          "success",
		RequestID:        requestID,
		TraceID:          result.GetTraceId(),
		ServerTimeUnixMs: time.Now().UnixMilli(),
		Data:             &uploadImageData{Asset: responseAsset},
	})
	logUploadInfo("upload_image.success", requestID, "asset_id", asset.ID, "image_url", asset.PublicURL, "size_bytes", asset.SizeBytes, "mime_type", asset.MimeType, "duration_ms", time.Since(startedAt).Milliseconds())
}

func (h *uploadHandler) handleImageDelete(w http.ResponseWriter, r *http.Request) {
	requestID := uploadRequestID(r)
	traceID := uploadTraceID(r, requestID)
	if requestctx.RequestID(r.Context()) == "" {
		r = r.WithContext(requestctx.WithRequestContext(r.Context(), requestctx.RequestContext{RequestID: requestID, TraceID: traceID, ServerTimeMs: time.Now().UnixMilli()}))
	}
	w.Header().Set(requestctx.HeaderRequestID, requestID)
	w.Header().Set(requestctx.HeaderTraceID, traceID)
	w.Header().Set(requestctx.HeaderServerTime, fmt.Sprintf("%d", time.Now().UnixMilli()))
	if r.Method != http.MethodDelete {
		writeUploadError(w, http.StatusMethodNotAllowed, r.Context(), fmt.Errorf("%w: method not allowed", apperr.ErrInvalidArgument), nil)
		return
	}
	ctx, claims, err := h.authenticate(r.Context(), r)
	if err != nil {
		writeUploadError(w, uploadAuthStatus(err), ctx, err, nil)
		return
	}
	r = r.WithContext(ctx)
	if h.store == nil {
		writeUploadError(w, http.StatusServiceUnavailable, r.Context(), errors.New("asset store is not configured"), nil)
		return
	}
	assetID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/uploads/images/"), "/")
	if assetID == "" {
		writeUploadError(w, http.StatusBadRequest, r.Context(), fmt.Errorf("%w: asset id is required", apperr.ErrInvalidArgument), nil)
		return
	}
	asset, err := h.store.FindTemporaryAssetForDelete(r.Context(), assetID, claims.UserID)
	if err != nil {
		writeUploadError(w, http.StatusNotFound, r.Context(), fmt.Errorf("%w: temporary asset not found or already attached", apperr.ErrNotFound), err)
		return
	}
	if h.storage != nil {
		if err := h.storage.DeleteObject(r.Context(), asset.ObjectKey); err != nil {
			writeUploadError(w, http.StatusBadGateway, r.Context(), errors.New("delete image from object storage failed"), err)
			return
		}
	}
	if err := h.store.MarkAssetDeleted(r.Context(), asset.ID, claims.UserID); err != nil {
		writeUploadError(w, http.StatusInternalServerError, r.Context(), errors.New("mark asset deleted failed"), err)
		return
	}
	result := appsvc.OKResult(r.Context())
	writeJSON(w, http.StatusOK, uploadImageResponse{Result: result, Code: 0, Message: "success", RequestID: requestID, TraceID: result.GetTraceId(), ServerTimeUnixMs: time.Now().UnixMilli()})
	logUploadInfo("upload_image.deleted", requestID, "asset_id", asset.ID, "object_key", asset.ObjectKey)
}

func (h *uploadHandler) authenticate(ctx context.Context, r *http.Request) (context.Context, *auth.Claims, error) {
	if h.auth == nil {
		return ctx, nil, errors.New("auth manager is not configured")
	}
	value := r.Header.Get("Authorization")
	if value == "" {
		value = r.Header.Get("authorization")
	}
	ctx = h.auth.WithAuthContextFromBearer(ctx, value)
	claims, err := auth.RequirePermission(ctx, userbiz.PermissionUploadImage)
	if err != nil {
		return ctx, nil, err
	}
	return ctx, claims, nil
}

func writeUploadError(w http.ResponseWriter, status int, ctx context.Context, publicErr error, cause error) {
	result := appsvc.ErrorResult(ctx, publicErr)
	requestID := requestctx.RequestID(ctx)
	if requestID == "" {
		requestID = result.GetTraceId()
	}
	if cause == nil {
		cause = publicErr
	}
	logUploadError("upload_image.failure", requestID, "status", status, "code", result.GetCode(), "message", result.GetMessage(), "cause", cause)
	writeJSON(w, status, uploadImageResponse{
		Result:           result,
		Code:             result.GetCode(),
		Message:          result.GetMessage(),
		RequestID:        requestID,
		TraceID:          result.GetTraceId(),
		ServerTimeUnixMs: time.Now().UnixMilli(),
	})
}

func logUploadInfo(event, requestID string, attrs ...any) {
	args := append([]any{"event", event, "request_id", requestID}, attrs...)
	slog.Info("auction upload", args...)
}

func logUploadError(event, requestID string, attrs ...any) {
	args := append([]any{"event", event, "request_id", requestID}, attrs...)
	slog.Error("auction upload", args...)
}

func uploadStorageTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("AUCTION_UPLOAD_STORAGE_TIMEOUT"))
	if raw == "" {
		return 30 * time.Second
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 30 * time.Second
	}
	return parsed
}

func uploadRequestID(r *http.Request) string {
	if requestID := requestctx.RequestID(r.Context()); requestID != "" {
		return requestID
	}
	for _, key := range []string{requestctx.HeaderRequestID, "X-Request-ID", requestctx.HeaderTraceID} {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			return value
		}
	}
	return idgen.New("req")
}

func uploadTraceID(r *http.Request, requestID string) string {
	if traceID := requestctx.TraceID(r.Context()); traceID != "" {
		return traceID
	}
	if traceID := strings.TrimSpace(r.Header.Get(requestctx.HeaderTraceID)); traceID != "" {
		return traceID
	}
	return requestID
}

func uploadAuthStatus(err error) int {
	if apperr.IsPermissionDenied(err) {
		return http.StatusForbidden
	}
	if apperr.IsUnauthenticated(err) || apperr.IsTokenExpired(err) || apperr.IsSessionExpired(err) || apperr.IsInvalidToken(err) {
		return http.StatusUnauthorized
	}
	return http.StatusServiceUnavailable
}

func validateImageBytes(data []byte) (string, string, error) {
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg", "jpg", nil
	}
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return "image/png", "png", nil
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp", "webp", nil
	}
	return "", "", errors.New("only jpeg, png and webp images are allowed")
}

var bizTypeRe = regexp.MustCompile(`[^a-zA-Z0-9_\-]`)

func sanitizeBizType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = bizTypeRe.ReplaceAllString(value, "_")
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}
