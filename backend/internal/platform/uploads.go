package platform

import (
	"bytes"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

var localImage = regexp.MustCompile(`^/media/img_[a-f0-9]{32}\.jpg$`)

func (s *Server) upload(w http.ResponseWriter, r *http.Request, u *User) error {
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		return fail(429, "图片处理中，请稍后再上传")
	}
	if !s.limits.allow("upload:"+u.ID, 15) {
		return fail(429, "上传太频繁，请稍后重试")
	}
	if e := r.ParseMultipartForm(6 << 20); e != nil {
		return fail(400, "请选择 5 MB 以内的图片")
	}
	defer r.MultipartForm.RemoveAll()
	f, _, e := r.FormFile("image")
	if e != nil {
		return fail(400, "请上传图片")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 5<<20+1))
	if e != nil {
		return e
	}
	if len(b) > 5<<20 {
		return fail(413, "图片不能超过 5 MB")
	}
	config, format, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || (format != "jpeg" && format != "png") || config.Width < 1 || config.Height < 1 || config.Width > 6000 || config.Height > 6000 || int64(config.Width)*int64(config.Height) > 16000000 {
		return fail(400, "图片必须为 JPG/PNG，最长边不超过 6000 像素、总像素不超过 1600 万")
	}
	decoded, _, e := image.Decode(bytes.NewReader(b))
	if e != nil {
		return fail(400, "无法读取这张图片")
	}
	if e = os.MkdirAll(s.cfg.UploadDir, 0700); e != nil {
		return e
	}
	name := id("img_") + ".jpg"
	path := filepath.Join(s.cfg.UploadDir, name)
	out, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	// Re-encode to remove EXIF (including location), embedded metadata and payloads.
	err := jpeg.Encode(out, decoded, &jpeg.Options{Quality: 88})
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		os.Remove(path)
		if err != nil {
			return err
		}
		return closeErr
	}
	return result(w, 201, map[string]string{"url": "/media/" + name})
}
func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !localImage.MatchString("/media/" + name) {
		http.NotFound(w, r)
		return
	}
	file, e := os.Open(filepath.Join(s.cfg.UploadDir, name))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	stat, e := file.Stat()
	if e != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, stat.ModTime(), file)
}
