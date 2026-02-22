package tbox

import (
	"encoding/json"
	"strconv"
	"time"
)

// FlexInt64 is an int64 that accepts both JSON numbers and JSON strings.
// The Tencent Box API inconsistently returns versionId as either type.
type FlexInt64 int64

func (f *FlexInt64) UnmarshalJSON(data []byte) error {
	// Try number first.
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*f = FlexInt64(n)
		return nil
	}
	// Try quoted string.
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*f = FlexInt64(n)
	return nil
}

// SpaceCred holds the access token and space identifiers returned by the space API.
type SpaceCred struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int64  `json:"expiresIn"`
	LibraryId   string `json:"libraryId"`
	SpaceId     string `json:"spaceId"`
	Status      int64  `json:"status"`
	Message     string `json:"message"`
}

// SpaceQuotaInfo holds quota information for the personal space.
type SpaceQuotaInfo struct {
	AvailableSpace string `json:"availableSpace"`
	Capacity       string `json:"capacity"`
	Size           string `json:"size"`
}

// FileInfo holds metadata for a single file.
type FileInfo struct {
	AuthorityList    interface{} `json:"authorityList"`
	ContentType      string      `json:"contentType"`
	Crc64            string      `json:"crc64"`
	CreationTime     time.Time   `json:"creationTime"`
	ETag             string      `json:"eTag"`
	ModificationTime time.Time   `json:"modificationTime"`
	Name             string      `json:"name"`
	Path             []string    `json:"path"`
	Size             string      `json:"size"`
	Type             string      `json:"type"`
	VersionId        *FlexInt64  `json:"versionId"`
}

// FolderInfo holds metadata for a directory.
type FolderInfo struct {
	CreationTime     time.Time `json:"creationTime"`
	ETag             string    `json:"eTag"`
	ModificationTime time.Time `json:"modificationTime"`
	Name             string    `json:"name"`
	Path             []string  `json:"path"`
	Type             string    `json:"type"`
}

// MergedItemDto combines file and folder fields (used in directory listings).
type MergedItemDto struct {
	AuthorityList    interface{} `json:"authorityList"`
	ContentType      string      `json:"contentType"`
	Crc64            string      `json:"crc64"`
	CreationTime     time.Time   `json:"creationTime"`
	ETag             string      `json:"eTag"`
	ModificationTime time.Time   `json:"modificationTime"`
	Name             string      `json:"name"`
	Path             []string    `json:"path"`
	Size             string      `json:"size"`
	Type             string      `json:"type"`
	VersionId        *FlexInt64  `json:"versionId"`
}

// ItemListDto holds a paginated directory listing.
type ItemListDto struct {
	Contents    []MergedItemDto `json:"contents"`
	FileCount   int64           `json:"fileCount"`
	SubDirCount int64           `json:"subDirCount"`
	TotalNum    int64           `json:"totalNum"`
	Path        []string        `json:"path"`
}

// ChunkUploadPart holds per-chunk upload headers.
type ChunkUploadPart struct {
	Headers map[string]string `json:"headers"`
}

// StartChunkUploadResDto is returned when starting a multipart upload.
type StartChunkUploadResDto struct {
	ConfirmKey string                     `json:"confirmKey"`
	Domain     string                     `json:"domain"`
	Expiration time.Time                  `json:"expiration"`
	Parts      map[string]ChunkUploadPart `json:"parts"`
	Path       string                     `json:"path"`
	UploadId   string                     `json:"uploadId"`
	Code       string                     `json:"code"`
	Status     int64                      `json:"status"`
	Message    string                     `json:"message"`
}

// SimpleUploadInfoDto is returned when starting a simple (single-part) upload.
type SimpleUploadInfoDto struct {
	ConfirmKey string            `json:"confirmKey"`
	Domain     string            `json:"domain"`
	Expiration string            `json:"expiration"`
	Headers    map[string]string `json:"headers"`
	Path       string            `json:"path"`
}

// ConfirmUploadResDto is returned after confirming an upload.
type ConfirmUploadResDto struct {
	Name    string   `json:"name"`
	Path    []string `json:"path"`
	Size    string   `json:"size"`
	Type    string   `json:"type"`
	Status  int64    `json:"status"`
	Message string   `json:"message"`
}

// LoginResDto is returned after a successful jAccount SSO login.
type LoginResDto struct {
	ExpiresIn int64  `json:"expiresIn"`
	UserToken string `json:"userToken"`
	Status    int64  `json:"status"`
	Message   string `json:"message"`
}

// ErrorMessageDto is the generic error response shape from the Tbox API.
type ErrorMessageDto struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int64  `json:"status"`
}

// DeleteFileDto is returned after deleting a file or directory.
type DeleteFileDto struct {
	DirectoryCount int64 `json:"directoryCount"`
	FileCount      int64 `json:"fileCount"`
}

// MoveFileDto is returned after a move/copy operation.
type MoveFileDto struct {
	Path []string `json:"path"`
}
