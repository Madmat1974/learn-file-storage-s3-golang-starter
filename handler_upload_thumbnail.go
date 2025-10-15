package main

import (
	"io"
	"path/filepath"
	"mime"
	"net/http"
	"os"
	"crypto/rand"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
	"encoding/base64"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDStr := r.PathValue("videoID")
	vid, err := uuid.Parse(videoIDStr)
	if err != nil {
    	respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
    	return
	}
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	const maxMemory = 10 << 20 // 10 MB
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", nil)
		return
	}

	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
    	respondWithError(w, http.StatusInternalServerError, "Failed to generate random name", err)
    	return
	}
	name := base64.RawURLEncoding.EncodeToString(random)

	// derive extension
	ext := "jpeg"
	if mediaType == "image/png" {
    	ext = "png"
	}
	// build asset path (adjust folder to your convention)
	assetPath := "thumbnails/" + name + "." + ext
	assetDiskPath := cfg.getAssetDiskPath(assetPath)

	dir := filepath.Dir(assetDiskPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to prepare asset directory", err)
		return
	}

	dst, err := os.Create(assetDiskPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create file on server", err)
		return
	}
	defer dst.Close()
	if _, err = io.Copy(dst, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error saving file", err)
		return
	}

	video, err := cfg.db.GetVideo(vid)

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't find video", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not authorized to update this video", nil)
		return
	}

	url := cfg.getAssetURL(assetPath)
	video.ThumbnailURL = &url
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}

