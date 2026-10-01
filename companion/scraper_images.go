package main

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	_ "golang.org/x/image/webp"
)

// Reconstructed from the exact binary's scraper_images.go functions at
// 0x81d6e0, 0x81d920, and 0x81da80. The runtime decodes JPEG, PNG, and WebP;
// it writes logo artwork as PNG and other artwork as JPEG (quality 92).
func scraperValidateImage(data []byte) error {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		int64(config.Width)*int64(config.Height) >= 40_000_001 {
		return errors.New("图片格式无效或尺寸过大")
	}
	return nil
}

func scraperImageBytes(data []byte, artworkType string) ([]byte, error) {
	if err := scraperValidateImage(data); err != nil {
		return nil, err
	}

	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("无法解码图片")
	}

	var encoded bytes.Buffer
	if artworkType == "Logo" {
		if err := png.Encode(&encoded, decoded); err != nil {
			return nil, errors.New("图片编码失败")
		}
	} else if err := jpeg.Encode(&encoded, decoded, &jpeg.Options{Quality: 92}); err != nil {
		return nil, errors.New("图片编码失败")
	}
	return encoded.Bytes(), nil
}

func scraperEncodeForPath(data []byte, path string) ([]byte, error) {
	extension := ""
	if dot := strings.LastIndex(path, "."); dot > strings.LastIndex(path, "/") {
		extension = strings.ToLower(path[dot:])
	}
	switch extension {
	case ".jpg", ".jpeg":
		return scraperImageBytes(data, "Poster")
	case ".png":
		return scraperImageBytes(data, "Logo")
	default:
		return nil, errors.New("仅支持写入 JPG/PNG 图片")
	}
}
