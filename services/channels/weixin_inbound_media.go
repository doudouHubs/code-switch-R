package channels

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// downloadInboundImage 同时兼容微信当前 CDN 媒体协议和旧版 file_id 接口。
// 新协议的 image_item 通常只提供 media.encrypt_query_param，不能再只盯着 file_id。
func (p *weixinProvider) downloadInboundImage(ctx context.Context, messageID string, item map[string]any) ([]byte, string, error) {
	media := weixinMediaMap(item, "media")
	if firstString(media, "encrypt_query_param") == "" {
		// 某些消息只有缩略图引用；与 OpenCowork 保持相同优先级，完整媒体优先，
		// 只有完整媒体没有下载引用时才使用 thumb_media。
		media = weixinMediaMap(item, "thumb_media")
	}

	if query := firstString(media, "encrypt_query_param"); query != "" {
		data, contentType, err := p.downloadWeixinCDNMedia(ctx, query)
		if err != nil {
			return nil, "", err
		}

		key, err := resolveWeixinInboundAESKey(item, media)
		if err != nil {
			return nil, "", err
		}
		if len(key) > 0 {
			// CDN 返回的是 AES-128-ECB 密文；先解密再识别文件头，否则
			// application/octet-stream 响应无法进入模型的图片输入协议。
			data, err = decryptWeixinECB(data, key)
			if err != nil {
				return nil, "", fmt.Errorf("decrypt Weixin inbound image: %w", err)
			}
		}
		mediaType, err := detectWeixinImageMediaType(data, contentType)
		if err != nil {
			return nil, "", err
		}
		return data, mediaType, nil
	}

	if firstString(item, "file_id") != "" {
		return p.downloadLegacyWeixinImage(ctx, messageID, item)
	}
	return nil, "", fmt.Errorf("missing Weixin inbound image reference")
}

func (p *weixinProvider) downloadWeixinCDNMedia(ctx context.Context, encryptedQueryParam string) ([]byte, string, error) {
	endpoint := strings.TrimRight(p.cdnBaseURL, "/") + "/download?encrypted_query_param=" + url.QueryEscape(encryptedQueryParam)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("Weixin CDN image download failed: %s", response.Status)
	}
	data, err := ioReadLimit(response.Body, channelMaxHTTPBody)
	if err != nil {
		return nil, "", err
	}
	return data, response.Header.Get("Content-Type"), nil
}

func (p *weixinProvider) downloadLegacyWeixinImage(ctx context.Context, messageID string, item map[string]any) ([]byte, string, error) {
	body := map[string]any{
		"message_id": messageID,
		"file_id":    firstString(item, "file_id"),
		"aes_key":    firstString(item, "aes_key", "aeskey"),
		"md5sum":     firstString(item, "md5sum"),
		"file_name":  firstString(item, "file_name"),
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/ilink/bot/downloadmessageimage", mustReader(body))
	if err != nil {
		return nil, "", err
	}
	for key, value := range p.headers() {
		request.Header.Set(key, value)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("Weixin image download failed: %s", response.Status)
	}
	data, err := ioReadLimit(response.Body, channelMaxHTTPBody)
	if err != nil {
		return nil, "", err
	}
	mediaType, err := detectWeixinImageMediaType(data, response.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", err
	}
	return data, mediaType, nil
}

func weixinMediaMap(item map[string]any, key string) map[string]any {
	media, _ := item[key].(map[string]any)
	return media
}

func resolveWeixinInboundAESKey(item, media map[string]any) ([]byte, error) {
	// aeskey 是微信消息里的 32 位十六进制原始密钥；media.aes_key
	// 通常是 base64，兼容其中携带十六进制文本的原版格式。
	if rawHex := firstString(item, "aeskey"); rawHex != "" {
		if len(rawHex) != aes.BlockSize*2 {
			return nil, fmt.Errorf("invalid Weixin image aeskey length")
		}
		key, err := hex.DecodeString(rawHex)
		if err != nil || len(key) != aes.BlockSize {
			return nil, fmt.Errorf("invalid Weixin image aeskey format")
		}
		return key, nil
	}

	encoded := firstString(media, "aes_key")
	if encoded == "" {
		encoded = firstString(item, "aes_key")
	}
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid Weixin image aes_key: %w", err)
	}
	if len(decoded) == aes.BlockSize {
		return decoded, nil
	}
	if len(decoded) == aes.BlockSize*2 {
		key, hexErr := hex.DecodeString(string(decoded))
		if hexErr == nil && len(key) == aes.BlockSize {
			return key, nil
		}
	}
	return nil, fmt.Errorf("invalid Weixin image aes_key length")
}

func detectWeixinImageMediaType(data []byte, fallback string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(fallback))
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(fallback, ";", 2)[0]))
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if isSupportedWeixinImageMediaType(mediaType) {
		return mediaType, nil
	}
	if sniffed := sniffWeixinImageMediaType(data); sniffed != "" {
		return sniffed, nil
	}
	return "", fmt.Errorf("Weixin image payload has unsupported media type %q", mediaType)
}

func sniffWeixinImageMediaType(data []byte) string {
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4e && data[3] == 0x47 && data[4] == 0x0d && data[5] == 0x0a && data[6] == 0x1a && data[7] == 0x0a {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
		return "image/gif"
	}
	if len(data) >= 12 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' && data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "image/webp"
	}
	return ""
}

func isSupportedWeixinImageMediaType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func weixinImageReference(item map[string]any) string {
	if value := firstString(item, "file_id"); value != "" {
		return value
	}
	if media := weixinMediaMap(item, "media"); media != nil {
		if value := firstString(media, "encrypt_query_param"); value != "" {
			// encrypt_query_param 是签名参数，不能原样回显到 UI 或频道消息。
			return "cdn-media"
		}
	}
	if media := weixinMediaMap(item, "thumb_media"); media != nil {
		if value := firstString(media, "encrypt_query_param"); value != "" {
			return "cdn-thumbnail"
		}
	}
	return "unknown"
}
