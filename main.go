package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type JobStatus struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Progress    int    `json:"progress"`
	FileInfo    string `json:"file_info,omitempty"`
	FFmpegError string `json:"ffmpeg_error,omitempty"`
}

var jobStatuses = make(map[string]*JobStatus)
var jobLogs = make(map[string]string)
var mu sync.Mutex

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	file, _, err := r.FormFile("videoFile")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	err = os.MkdirAll("uploads", os.ModePerm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tempFile, err := os.CreateTemp("uploads", "upload-*.mp4")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id := uuid.New().String()
	jobStatus := &JobStatus{ID: id, Status: "processing", Progress: 0}
	mu.Lock()
	jobStatuses[id] = jobStatus
	mu.Unlock()

	go processVideo(id, tempFile.Name())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func processVideo(id, filePath string) {
	updateJobStatus(id, "processing", 25, "", "")

	fileInfo, err := analyzeVideo(filePath)
	if err != nil {
		updateJobStatus(id, "error", 0, "", "")
		return
	}

	updateJobStatus(id, "processing", 50, "", "")

	ffmpegLog, ffmpegError, err := getFFmpegLog(filePath)
	if err != nil {
		updateJobStatus(id, "error", 0, "", "")
		return
	}

	mu.Lock()
	jobLogs[id] = ffmpegLog
	mu.Unlock()

	updateJobStatus(id, "completed", 100, fileInfo, ffmpegError)
}

func updateJobStatus(id, status string, progress int, fileInfo, ffmpegError string) {
	mu.Lock()
	defer mu.Unlock()
	jobStatuses[id].Status = status
	jobStatuses[id].Progress = progress
	jobStatuses[id].FileInfo = fileInfo
	jobStatuses[id].FFmpegError = ffmpegError
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing id parameter", http.StatusBadRequest)
		return
	}

	mu.Lock()
	jobStatus, exists := jobStatuses[id]
	mu.Unlock()

	if !exists {
		http.Error(w, "Invalid id", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobStatus)
}

func logHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing id parameter", http.StatusBadRequest)
		return
	}

	mu.Lock()
	log, exists := jobLogs[id]
	mu.Unlock()

	if !exists {
		http.Error(w, "Invalid id", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(log))
}

func analyzeVideo(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", filePath)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func getFFmpegLog(filePath string) (string, string, error) {
	cmd := exec.Command("ffmpeg", "-i", filePath, "-vf", "showinfo", "-f", "null", "-")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", err
	}

	var importantLines []string
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "error") || strings.Contains(line, "warning") || strings.Contains(line, "NAL unit") || strings.Contains(line, "decode") {
			importantLines = append(importantLines, line)
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return "", "", scanErr
	}

	return string(output), strings.Join(importantLines, "\n"), nil
}

func main() {
	srv := &http.Server{
		Addr:         ":5000",
		ReadTimeout:  10 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		Handler:      http.DefaultServeMux,
	}

	http.Handle("/", http.FileServer(http.Dir("./static")))
	http.HandleFunc("/upload", uploadHandler)
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/log", logHandler)

	fmt.Println("Server started at :5000")
	srv.ListenAndServe()
}
