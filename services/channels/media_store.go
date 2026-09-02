package channels

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const channelMediaDirectoryMode = 0o700

// MaterializeImage 将已经下载到内存的频道图片写入受控目录，并返回供内部
// Codex 请求使用的绝对路径。数据库 BLOB 仍由 SaveMedia 负责，二者故意分开，
// 这样文件系统故障不会破坏频道消息历史。
func (s *Store) MaterializeImage(messageID, instanceID string, media ChannelMedia) (string, error) {
	if s == nil {
		return "", errors.New("channel store is unavailable")
	}
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(instanceID) == "" {
		return "", errors.New("channel media requires message and instance")
	}
	if strings.ToLower(strings.TrimSpace(media.Kind)) != "image" {
		return "", errors.New("only image media can be materialized for Codex")
	}
	if len(media.Data) == 0 {
		return "", errors.New("channel image data is empty")
	}
	if len(media.Data) > channelMaxHTTPBody {
		return "", fmt.Errorf("channel image exceeds %d bytes", channelMaxHTTPBody)
	}
	mediaType := normalizeChannelImageMediaType(media.MediaType)
	if !isSupportedChannelImageMediaType(mediaType) {
		return "", fmt.Errorf("unsupported channel image media type %q", media.MediaType)
	}

	root := s.MediaRoot()
	if root == "" {
		return "", errors.New("channel media root is unavailable")
	}
	if err := os.MkdirAll(root, channelMediaDirectoryMode); err != nil {
		return "", fmt.Errorf("create channel media directory: %w", err)
	}
	path := filepath.Join(root, channelMediaFileName(messageID, media, mediaType))
	if err := writeChannelMediaAtomically(path, media.Data); err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}

func channelMediaStorageID(messageID string, media ChannelMedia) string {
	if id := strings.TrimSpace(media.ID); id != "" {
		return id
	}
	// 保持既有 channel_media 的 ID 算法，避免同一条历史消息在新版本中
	// 因为媒体类型字段变化而生成另一份媒体记录。
	return sessionKey(messageID, media.Kind, media.FileName)
}

func channelMediaFileName(messageID string, media ChannelMedia, mediaType string) string {
	// 文件名只使用哈希和受控扩展名，不把微信提供的 file_name 直接拼进路径，
	// 避免特殊字符、盘符或目录片段穿透媒体根目录。
	return sessionKey(messageID, channelMediaStorageID(messageID, media), mediaType) + channelMediaExtension(mediaType)
}

func channelMediaExtension(mediaType string) string {
	switch normalizeChannelImageMediaType(mediaType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

func normalizeChannelImageMediaType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if separator := strings.IndexByte(value, ';'); separator >= 0 {
		value = strings.TrimSpace(value[:separator])
	}
	return value
}

func isSupportedChannelImageMediaType(mediaType string) bool {
	switch normalizeChannelImageMediaType(mediaType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func writeChannelMediaAtomically(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".channel-media-*")
	if err != nil {
		return fmt.Errorf("create temporary channel media file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary channel media file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write channel media file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush channel media file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close channel media file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	} else if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, data) {
		// 重复 webhook 会生成同一路径；内容一致时直接复用，避免 Windows
		// 下 Rename 无法覆盖已存在目标而把幂等请求误判成失败。
		return nil
	} else {
		return fmt.Errorf("commit channel media file: %w", err)
	}
}
