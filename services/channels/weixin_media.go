package channels

import (
	"context"
	"crypto/aes"
	"crypto/md5"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type weixinUploadedMedia struct {
	DownloadEncryptedQueryParam string
	AESKeyHex                    string
	RawSize                      int
	CipherSize                   int
}

func weixinAESBlockSize(size int) int {
	padding := aes.BlockSize - size%aes.BlockSize
	if padding == 0 {
		padding = aes.BlockSize
	}
	return size + padding
}

func encryptWeixinECB(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	paddedSize := weixinAESBlockSize(len(data))
	padded := make([]byte, paddedSize)
	copy(padded, data)
	padding := byte(paddedSize - len(data))
	for index := len(data); index < len(padded); index++ {
		padded[index] = padding
	}
	result := make([]byte, len(padded))
	for offset := 0; offset < len(padded); offset += aes.BlockSize {
		block.Encrypt(result[offset:offset+aes.BlockSize], padded[offset:offset+aes.BlockSize])
	}
	return result, nil
}

func (p *weixinProvider) uploadWeixinMedia(ctx context.Context, chatID string, media ChannelMedia, mediaType int) (weixinUploadedMedia, error) {
	if len(media.Data) == 0 {
		return weixinUploadedMedia{}, errors.New("Weixin media is empty")
	}
	key := make([]byte, 16)
	if _, err := cryptorand.Read(key); err != nil {
		return weixinUploadedMedia{}, errors.New("Weixin media encryption key is unavailable")
	}
	fileKeyBytes := make([]byte, 16)
	if _, err := cryptorand.Read(fileKeyBytes); err != nil {
		return weixinUploadedMedia{}, errors.New("Weixin media file key is unavailable")
	}
	fileKey := hex.EncodeToString(fileKeyBytes)
	ciphertext, err := encryptWeixinECB(media.Data, key)
	if err != nil {
		return weixinUploadedMedia{}, err
	}
	checksum := md5.Sum(media.Data)
	aesKeyHex := hex.EncodeToString(key)
	var response struct {
		Ret              int    `json:"ret"`
		ErrCode          int    `json:"errcode"`
		ErrMsg           string `json:"errmsg"`
		UploadParam      string `json:"upload_param"`
		UploadFullURL    string `json:"upload_full_url"`
		Data             *struct {
			Ret           int    `json:"ret"`
			ErrCode       int    `json:"errcode"`
			ErrMsg        string `json:"errmsg"`
			UploadParam   string `json:"upload_param"`
			UploadFullURL string `json:"upload_full_url"`
		} `json:"data"`
	}
	body := map[string]any{
		"filekey":        fileKey,
		"media_type":     mediaType,
		"to_user_id":     chatID,
		"rawsize":        len(media.Data),
		"rawfilemd5":     hex.EncodeToString(checksum[:]),
		"filesize":       len(ciphertext),
		"no_need_thumb":  true,
		"aeskey":         aesKeyHex,
		"base_info":      map[string]string{"channel_version": "1.0.0"},
	}
	if err := p.post(ctx, "ilink/bot/getuploadurl", body, &response); err != nil {
		return weixinUploadedMedia{}, err
	}
	if response.Data != nil {
		if response.UploadParam == "" {
			response.UploadParam = response.Data.UploadParam
		}
		if response.UploadFullURL == "" {
			response.UploadFullURL = response.Data.UploadFullURL
		}
		if response.ErrCode == 0 {
			response.ErrCode = response.Data.ErrCode
		}
		if response.Ret == 0 {
			response.Ret = response.Data.Ret
		}
		if response.ErrMsg == "" {
			response.ErrMsg = response.Data.ErrMsg
		}
	}
	code := response.ErrCode
	if code == 0 {
		code = response.Ret
	}
	if code != 0 {
		return weixinUploadedMedia{}, fmt.Errorf("Weixin getuploadurl rejected: %s", response.ErrMsg)
	}
	uploadURL := strings.TrimSpace(response.UploadFullURL)
	if uploadURL == "" && strings.TrimSpace(response.UploadParam) != "" {
		uploadURL = strings.TrimRight(p.cdnBaseURL, "/") + "/upload?encrypted_query_param=" + url.QueryEscape(response.UploadParam) + "&filekey=" + url.QueryEscape(fileKey)
	}
	if uploadURL == "" {
		return weixinUploadedMedia{}, errors.New("Weixin getuploadurl returned no upload URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, strings.NewReader(string(ciphertext)))
	if err != nil {
		return weixinUploadedMedia{}, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	responseHTTP, err := p.client.Do(request)
	if err != nil {
		return weixinUploadedMedia{}, err
	}
	defer responseHTTP.Body.Close()
	if responseHTTP.StatusCode < http.StatusOK || responseHTTP.StatusCode >= http.StatusMultipleChoices {
		return weixinUploadedMedia{}, fmt.Errorf("Weixin CDN upload failed: %s", responseHTTP.Status)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(responseHTTP.Body, 1<<20))
	downloadParam := strings.TrimSpace(responseHTTP.Header.Get("x-encrypted-param"))
	if downloadParam == "" {
		return weixinUploadedMedia{}, errors.New("Weixin CDN upload returned no encrypted parameter")
	}
	return weixinUploadedMedia{DownloadEncryptedQueryParam: downloadParam, AESKeyHex: aesKeyHex, RawSize: len(media.Data), CipherSize: len(ciphertext)}, nil
}

func (p *weixinProvider) sendWeixinItems(ctx context.Context, chatID, contextToken string, items []map[string]any) (string, error) {
	if strings.TrimSpace(contextToken) == "" {
		return "", errors.New("Weixin reply context is not available")
	}
	clientID := ""
	for _, item := range items {
		clientID = fmt.Sprintf("%d", nowMillis())
		var response map[string]any
		body := map[string]any{"msg": map[string]any{
			"from_user_id": "", "to_user_id": chatID, "client_id": clientID,
			"message_type": 2, "message_state": 2, "item_list": []map[string]any{item},
			"context_token": contextToken,
		}}
		if err := p.post(ctx, "ilink/bot/sendmessage", body, &response); err != nil {
			return "", err
		}
		if value := firstString(response, "errcode", "ret"); value != "" && value != "0" {
			return "", errors.New("Weixin rejected sendMessage")
		}
	}
	return clientID, nil
}

func encodeWeixinAESKey(keyHex string) string {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return ""
	}
	return encodeBase64(key)
}

func encodeBase64(value []byte) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	result := make([]byte, 0, (len(value)+2)/3*4)
	for index := 0; index < len(value); index += 3 {
		remaining := len(value) - index
		var chunk uint32
		chunk = uint32(value[index]) << 16
		if remaining > 1 {
			chunk |= uint32(value[index+1]) << 8
		}
		if remaining > 2 {
			chunk |= uint32(value[index+2])
		}
		result = append(result, table[(chunk>>18)&63], table[(chunk>>12)&63])
		if remaining > 1 {
			result = append(result, table[(chunk>>6)&63])
		} else {
			result = append(result, '=')
		}
		if remaining > 2 {
			result = append(result, table[chunk&63])
		} else {
			result = append(result, '=')
		}
	}
	return string(result)
}

func (p *weixinProvider) SendWeixinImage(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	if !isSupportedWeixinImage(media.Data) {
		return "", errors.New("The provided payload is not a supported image file")
	}
	uploaded, err := p.uploadWeixinMedia(ctx, chatID, media, 1)
	if err != nil {
		return "", err
	}
	items := make([]map[string]any, 0, 2)
	if strings.TrimSpace(caption) != "" {
		items = append(items, map[string]any{"type": 1, "text_item": map[string]string{"text": caption}})
	}
	items = append(items, map[string]any{"type": 2, "image_item": map[string]any{
		"media": map[string]any{"encrypt_query_param": uploaded.DownloadEncryptedQueryParam, "aes_key": encodeWeixinAESKey(uploaded.AESKeyHex), "encrypt_type": 1},
		"mid_size": uploaded.CipherSize,
	}})
	return p.sendWeixinItems(ctx, chatID, p.contextToken(chatID), items)
}

func (p *weixinProvider) SendWeixinFile(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	uploaded, err := p.uploadWeixinMedia(ctx, chatID, media, 3)
	if err != nil {
		return "", err
	}
	fileName := strings.TrimSpace(media.FileName)
	if fileName == "" {
		fileName = "file"
	}
	items := make([]map[string]any, 0, 2)
	if strings.TrimSpace(caption) != "" {
		items = append(items, map[string]any{"type": 1, "text_item": map[string]string{"text": caption}})
	}
	items = append(items, map[string]any{"type": 4, "file_item": map[string]any{
		"media": map[string]any{"encrypt_query_param": uploaded.DownloadEncryptedQueryParam, "aes_key": encodeWeixinAESKey(uploaded.AESKeyHex), "encrypt_type": 1},
		"file_name": fileName,
		"len":       fmt.Sprint(uploaded.RawSize),
	}})
	return p.sendWeixinItems(ctx, chatID, p.contextToken(chatID), items)
}

func isSupportedWeixinImage(data []byte) bool {
	if len(data) >= 4 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4e && data[3] == 0x47 {
		return true
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return true
	}
	if len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
		return true
	}
	if len(data) >= 12 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' && data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return true
	}
	return false
}
