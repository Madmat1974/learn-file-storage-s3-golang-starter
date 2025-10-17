package main

import (
	"net/http"
	"github.com/google/uuid"
	"encoding/json"
	"mime"
	"bytes"
	"io"
	"os"
	"os/exec"
	"crypto/rand"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"encoding/hex"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"fmt"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxMemory)
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
	//get video metadata by video uuid
	metaVid, err := cfg.db.GetVideo(vid)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Unable to find video", err)
		return
	}
	//verify video userID matches the user
	if metaVid.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized user", err)
		return
	}

	upfile, multiheader, err := r.FormFile("video")
	if err != nil {
		respondWithError(w,http.StatusBadRequest,"Unable to get file data", err)
		return
	}

	defer upfile.Close()
	//get video format type and verify
	mediaType, _, err := mime.ParseMediaType(multiheader.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", nil)
		return
	}

	tempfile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create temp file", err)
		return 
	}


	defer os.Remove(tempfile.Name())
	defer tempfile.Close()

	if _, err := io.Copy(tempfile, upfile); err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to upload", err)
		return 
	}
	tempfile.Sync()

	//grab aspect ratio if available
	aspect, err := getVideoAspectRatio(tempfile.Name())
	if err != nil {
		respondWithError(w,http.StatusBadRequest, "Unable to read file data", err)
	}
	if _, err := tempfile.Seek(0, io.SeekStart); err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to rewind temp file", err)
		return 
	}
	if aspect == "16:9" {
		aspect = "landscape/"
	}
	if aspect == "9:16" {
		aspect = "portrait/"
	}
	if aspect == "other" {
		aspect = "other/"
	}
	processedPath, err := processVideoForFastStart(tempfile.Name())
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to implement faster file sequence", err)
		return
	}
	defer os.Remove(processedPath)

	pf, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to open", err)
	}

	if _, err := pf.Seek(0, io.SeekStart); err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to read file", err)
		return
	}

	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to generate random name", err)
		return
	}
	hexStr := hex.EncodeToString(random)
	generatedKey := aspect + hexStr + ".mp4"

	_, poo := cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:			aws.String(cfg.s3Bucket),
		Key:			aws.String(generatedKey),
		Body:			pf,
		ContentType:	aws.String(mediaType),	
	})
	if poo != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to upload to S3", poo)
		return 
	}

	url := fmt.Sprintf("https://" + cfg.s3Bucket + ".s3." + cfg.s3Region + ".amazonaws.com/" + generatedKey)
	videoURL := &url

	v := database.Video{
    ID:           vid,
    ThumbnailURL: metaVid.ThumbnailURL,
    VideoURL:     videoURL, // *string
    CreateVideoParams: database.CreateVideoParams{
        Title:       metaVid.Title,
        Description: metaVid.Description,
        UserID:      metaVid.UserID,
    },
}

	if err = cfg.db.UpdateVideo(v); err != nil {
		respondWithError(w, http.StatusNotFound, "Unable to upload video", err)
		return
	}
	respondWithJSON(w, http.StatusOK, vid)
}

func getVideoAspectRatio(filePath string) (string, error) {
	type Probe struct {
		Streams []struct {
			Width int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	//create an instance of struct
	var p Probe

	var buf bytes.Buffer

	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	cmd.Stdout = &buf

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffprobe failed: %w; out%s", err, buf.String())
	}


	data := buf.Bytes() //now has JSON that from ffprobe that can be unmarshalled
	
	boo := json.Unmarshal(data, &p)
	if boo != nil {
		return "", boo
	}
	if len(p.Streams) < 1 {
		return "", boo
	}
	if p.Streams[0].Width < 1 || p.Streams[0].Width < 1 {
		return "", boo
	}

	if p.Streams[0].Height > p.Streams[0].Width {
		result := p.Streams[0].Height*9/16
		widthmax := p.Streams[0].Width + 1
		widthmin := p.Streams[0].Width -1

		if result <= widthmax || result >= widthmin {
			return "9:16", nil
		}
	}
	if p.Streams[0].Height < p.Streams[0].Width {
		result := p.Streams[0].Height*16/9
		widthmax := p.Streams[0].Width + 1
		widthmin := p.Streams[0].Width -1
		if result <= widthmax || result >= widthmin {
			return "16:9", nil
		}
	}
	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	appendstring := ".processing"
	outputPath := filePath + appendstring
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg failed: %w", err)
	}
	return outputPath, nil
}