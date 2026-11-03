// Package media 保存并读取公开商品图片。图片会重新编码为 JPEG 以移除 EXIF 与嵌入内容。
package media

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/idgen"
)

var localImage = regexp.MustCompile(`^/media/img_[a-f0-9]{32}\.jpg$`)

type Store struct {
	dir string
}

func New(dir string) *Store { return &Store{dir: dir} }

func (s *Store) SaveJPEG(_ context.Context, data []byte) (string, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || config.Width < 1 || config.Height < 1 || config.Width > 6000 || config.Height > 6000 || int64(config.Width)*int64(config.Height) > 16000000 {
		return "", domain.Invalid("图片必须为 JPG/PNG，最长边不超过 6000 像素、总像素不超过 1600 万")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", domain.Invalid("无法读取这张图片")
	}
	if err = os.MkdirAll(s.dir, 0700); err != nil {
		return "", err
	}
	name := idgen.New("img_") + ".jpg"
	path := filepath.Join(s.dir, name)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	encodeErr := jpeg.Encode(out, decoded, &jpeg.Options{Quality: 88})
	if encodeErr == nil {
		encodeErr = out.Sync()
	}
	closeErr := out.Close()
	if encodeErr != nil || closeErr != nil {
		os.Remove(path)
		if encodeErr != nil {
			return "", encodeErr
		}
		return "", closeErr
	}
	return "/media/" + name, nil
}

func (s *Store) Open(name string) (io.ReadSeeker, time.Time, error) {
	if !localImage.MatchString("/media/" + name) {
		return nil, time.Time{}, os.ErrNotExist
	}
	file, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return nil, time.Time{}, err
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, time.Time{}, err
	}
	return file, stat.ModTime(), nil
}
