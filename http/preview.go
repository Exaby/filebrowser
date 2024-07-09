package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gorilla/mux"

	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/img"
)

/*
ENUM(
thumb
big
)
*/
type PreviewSize int

type ImgService interface {
	FormatFromExtension(ext string) (img.Format, error)
	Resize(ctx context.Context, in io.Reader, width, height int, out io.Writer, options ...img.Option) error
}

type FileCache interface {
	Store(ctx context.Context, key string, value []byte) error
	Load(ctx context.Context, key string) ([]byte, bool, error)
	Delete(ctx context.Context, key string) error
}

func previewHandler(imgSvc ImgService, fileCache FileCache, enableThumbnails, resizePreview bool) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if !d.user.Perm.Download {
			return http.StatusAccepted, nil
		}
		vars := mux.Vars(r)

		previewSize, err := ParsePreviewSize(vars["size"])
		if err != nil {
			return http.StatusBadRequest, err
		}

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       "/" + vars["path"],
			Modify:     d.user.Perm.Modify,
			Expand:     true,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err != nil {
			return errToStatus(err), err
		}

		setContentDisposition(w, r, file)

		switch file.Type {
		case "image":
			return handleImagePreview(w, r, imgSvc, fileCache, file, previewSize, enableThumbnails, resizePreview)
		case "video":
			return handleVideoPreview(w, r, fileCache, file, previewSize)
		default:
			return http.StatusNotImplemented, fmt.Errorf("can't create preview for %s type", file.Type)
		}
	})
}

func handleImagePreview(
	w http.ResponseWriter,
	r *http.Request,
	imgSvc ImgService,
	fileCache FileCache,
	file *files.FileInfo,
	previewSize PreviewSize,
	enableThumbnails, resizePreview bool,
) (int, error) {
	if (previewSize == PreviewSizeBig && !resizePreview) ||
		(previewSize == PreviewSizeThumb && !enableThumbnails) {
		return rawFileHandler(w, r, file)
	}

	format, err := imgSvc.FormatFromExtension(file.Extension)
	// Unsupported extensions directly return the raw data
	if errors.Is(err, img.ErrUnsupportedFormat) || format == img.FormatGif {
		return rawFileHandler(w, r, file)
	}
	if err != nil {
		return errToStatus(err), err
	}

	cacheKey := previewCacheKey(file, previewSize)
	resizedImage, ok, err := fileCache.Load(r.Context(), cacheKey)
	if err != nil {
		return errToStatus(err), err
	}
	if !ok {
		resizedImage, err = createImagePreview(imgSvc, fileCache, file, previewSize)
		if err != nil {
			return errToStatus(err), err
		}
	}

	w.Header().Set("Cache-Control", "private")
	http.ServeContent(w, r, file.Name, file.ModTime, bytes.NewReader(resizedImage))

	return 0, nil
}

func createImagePreview(
	imgSvc ImgService,
	fileCache FileCache,
	file *files.FileInfo,
	previewSize PreviewSize,
) ([]byte, error) {
	fd, err := file.Fs.Open(file.Path)
	if err != nil {
		return nil, err
	}
	defer fd.Close()

	var (
		width   int
		height  int
		options []img.Option
	)

	switch {
	case previewSize == PreviewSizeBig:
		width = 1080
		height = 1080
		options = append(options, img.WithMode(img.ResizeModeFit), img.WithQuality(img.QualityMedium))
	case previewSize == PreviewSizeThumb:
		width = 256
		height = 256
		options = append(options, img.WithMode(img.ResizeModeFill), img.WithQuality(img.QualityLow), img.WithFormat(img.FormatJpeg))
	default:
		return nil, img.ErrUnsupportedFormat
	}

	buf := &bytes.Buffer{}
	if err := imgSvc.Resize(context.Background(), fd, width, height, buf, options...); err != nil {
		return nil, err
	}

	go func() {
		cacheKey := previewCacheKey(file, previewSize)
		if err := fileCache.Store(context.Background(), cacheKey, buf.Bytes()); err != nil {
			fmt.Printf("failed to cache resized image: %v", err)
		}
	}()

	return buf.Bytes(), nil
}

func handleVideoPreview(
	w http.ResponseWriter,
	r *http.Request,
	fileCache FileCache,
	file *files.FileInfo,
	previewSize PreviewSize,
) (int, error) {
	cacheKey := previewCacheKey(file, previewSize)
	thumbnail, ok, err := fileCache.Load(r.Context(), cacheKey)
	if err != nil {
		log.Printf("Error loading thumbnail from cache for file %s: %v", file.Path, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return http.StatusInternalServerError, err
	}
	if !ok {
		thumbnail, err = createVideoThumbnail(file, previewSize, fileCache)
		if err != nil {
			log.Printf("Error creating video thumbnail for file %s: %v", file.Path, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return http.StatusInternalServerError, err
		}
	}

	w.Header().Set("Cache-Control", "private")
	http.ServeContent(w, r, file.Name, file.ModTime, bytes.NewReader(thumbnail))
	return http.StatusOK, nil
}

func createVideoThumbnail(file *files.FileInfo, previewSize PreviewSize, fileCache FileCache) ([]byte, error) {
	fd, err := file.Fs.Open(file.Path)
	if err != nil {
		log.Printf("Error opening file %s: %v", file.Path, err)
		return nil, err
	}
	defer fd.Close()

	tmpFile, err := os.CreateTemp("", "video-thumbnail-*.jpg")
	if err != nil {
		log.Printf("Error creating temporary file: %v", err)
		return nil, err
	}
	defer os.Remove(tmpFile.Name())

	log.Printf("Creating thumbnail for file: %s", file.Path)

	//exePath, err := os.Executable()
	if err != nil {
		log.Printf("Error getting executable path: %v", err)
		return nil, err
	}
	//exeDir := filepath.Dir(exePath)

	// replace  with /srv/ for docker
	absPath := filepath.Join("/srv/", file.Path) //filepath.Join(exeDir, file.Path)

	log.Printf("File path: %s, Absolute path: %s", file.Path, absPath)

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		log.Printf("File does not exist: %s", absPath)
		return nil, fmt.Errorf("file does not exist: %s", absPath)
	}

	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		if _, err := os.Stat(tmpFile.Name()); err == nil {
			err = os.Remove(tmpFile.Name())
			if err != nil {
				log.Printf("Error deleting existing temporary file (attempt %d/%d): %v", i+1, maxRetries, err)
				time.Sleep(100 * time.Millisecond) // Wait before retrying
				continue
			}
			break
		}
	}

	cmd := exec.Command("ffmpeg", "-y", "-i", absPath, "-ss", "00:00:01.000", "-vframes", "1", tmpFile.Name())
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Error running ffmpeg command: %v, output: %s", err, string(output))
		return nil, err
	}

	log.Printf("ffmpeg output: %s", string(output))

	thumbnail, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		log.Printf("Error reading temporary file: %v", err)
		return nil, err
	}
	cacheKey := previewCacheKey(file, previewSize)
	if err := fileCache.Store(context.Background(), cacheKey, thumbnail); err != nil {
		log.Printf("Error storing thumbnail in cache: %v", err)
	}

	return thumbnail, nil
}

func previewCacheKey(f *files.FileInfo, previewSize PreviewSize) string {
	return fmt.Sprintf("%x%x%x", f.RealPath(), f.ModTime.Unix(), previewSize)
}
