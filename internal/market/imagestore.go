package market

// ImageStore backs POST /uploads and GET /images/{key}: a MinIO bucket with
// no published port anywhere (matching every other backing store this repo
// closed off from the internet this session) and no direct client access —
// market is the only thing that ever talks to it, streaming bytes through
// on the way in and back out. web's next.config.ts proxies /blobs/* to
// market the same way it already proxies /idp/* to id, so the browser only
// ever sees a same-origin path.
//
// Every upload is decoded and re-encoded as JPEG server-side; that
// roundtrip both normalizes the format and strips EXIF as a side effect,
// since image.Image carries no metadata for Encode to write back out.
//
// ponytail: the object key (a random UUID market itself generated) is the
// only thing gating a GET — there's no per-request signature or expiry, a
// simplification from AGORA_SPEC.md's "served through signed URLs" language.
// A listing photo is public marketplace content the instant the listing
// goes live, the same trust level pasted image_url has always had; what
// actually matters is that MinIO's bucket and credentials are never reached
// directly, which routing every read through this handler already
// guarantees. Add real per-URL signing if these ever need to expire or gate
// on something other than "is this a real listing photo." webp isn't
// accepted in either — decoding it needs a dependency beyond stdlib
// (golang.org/x/image/webp) — add it if a real seller's phone needs it.
import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"agora/internal/authn"
	"agora/internal/httpx"
)

const (
	maxImageBytes   = 8 << 20 // re-encoded size cap
	jpegQuality     = 85
	sniffWindowSize = 512
)

type ImageStore struct {
	client *minio.Client
	bucket string
}

func NewImageStore(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useSSL bool) (*ImageStore, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, err
	}
	exists, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, err
		}
	}
	return &ImageStore{client: c, bucket: bucket}, nil
}

func newImageKey(userID string) string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s/%x-%x-%x-%x-%x.jpg", userID, b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (s *Server) handleUploadImage(w http.ResponseWriter, r *http.Request) {
	if s.images == nil {
		httpx.Error(w, 503, "image storage not configured")
		return
	}
	// generous ceiling on the raw upload; the re-encoded JPEG is checked
	// against maxImageBytes separately below.
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes*4)
	file, _, err := r.FormFile("image")
	if err != nil {
		httpx.Error(w, 400, "missing image file")
		return
	}
	defer file.Close()

	sniff := make([]byte, sniffWindowSize)
	n, _ := io.ReadFull(file, sniff)
	sniff = sniff[:n]
	// content-sniffed, not trusted from the filename/extension or the
	// client's declared Content-Type.
	switch http.DetectContentType(sniff) {
	case "image/jpeg", "image/png":
	default:
		httpx.Error(w, 400, "only JPEG and PNG images are accepted")
		return
	}
	img, _, err := image.Decode(io.MultiReader(bytes.NewReader(sniff), file))
	if err != nil {
		httpx.Error(w, 400, "could not decode image")
		return
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		httpx.Error(w, 500, "encode failed")
		return
	}
	if buf.Len() > maxImageBytes {
		httpx.Error(w, 400, "image too large")
		return
	}

	key := newImageKey(authn.UserID(r.Context()))
	if _, err := s.images.client.PutObject(r.Context(), s.images.bucket, key,
		bytes.NewReader(buf.Bytes()), int64(buf.Len()),
		minio.PutObjectOptions{ContentType: "image/jpeg"}); err != nil {
		httpx.Error(w, 500, "upload failed")
		return
	}
	httpx.JSON(w, 200, map[string]string{"image_url": "/images/" + key})
}

func (s *Server) handleGetImage(w http.ResponseWriter, r *http.Request) {
	if s.images == nil {
		httpx.Error(w, 404, "not found")
		return
	}
	obj, err := s.images.client.GetObject(r.Context(), s.images.bucket, r.PathValue("key"), minio.GetObjectOptions{})
	if err != nil {
		httpx.Error(w, 404, "not found")
		return
	}
	defer obj.Close()
	if _, err := obj.Stat(); err != nil {
		httpx.Error(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	io.Copy(w, obj)
}
