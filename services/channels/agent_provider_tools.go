package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	"codeswitch/services"
)

const (
	channelToolFeishuSendImage         services.PetAgentToolName = ChannelToolFeishuSendImage
	channelToolFeishuSendFile          services.PetAgentToolName = ChannelToolFeishuSendFile
	channelToolFeishuListChatMembers   services.PetAgentToolName = ChannelToolFeishuListChatMembers
	channelToolFeishuAtMember          services.PetAgentToolName = ChannelToolFeishuAtMember
	channelToolFeishuSendUrgent        services.PetAgentToolName = ChannelToolFeishuSendUrgent
	channelToolFeishuBitableListApps   services.PetAgentToolName = ChannelToolFeishuBitableListApps
	channelToolFeishuBitableListTables services.PetAgentToolName = ChannelToolFeishuBitableListTables
	channelToolFeishuBitableListFields services.PetAgentToolName = ChannelToolFeishuBitableListFields
	channelToolFeishuBitableGetRecords services.PetAgentToolName = ChannelToolFeishuBitableGetRecords
	channelToolFeishuBitableCreate     services.PetAgentToolName = ChannelToolFeishuBitableCreate
	channelToolFeishuBitableUpdate     services.PetAgentToolName = ChannelToolFeishuBitableUpdate
	channelToolFeishuBitableDelete     services.PetAgentToolName = ChannelToolFeishuBitableDelete
	// agent_tools.go 使用带 Records 后缀的内部 dispatch 名称；保留别名，
	// 让通用执行器和 provider capability 描述共享同一组外部工具值。
	channelToolFeishuBitableCreateRecords services.PetAgentToolName = channelToolFeishuBitableCreate
	channelToolFeishuBitableUpdateRecords services.PetAgentToolName = channelToolFeishuBitableUpdate
	channelToolFeishuBitableDeleteRecords services.PetAgentToolName = channelToolFeishuBitableDelete
	channelToolWeixinSendImage            services.PetAgentToolName = ChannelToolWeixinSendImage
	channelToolWeixinSendFile             services.PetAgentToolName = ChannelToolWeixinSendFile

	channelToolRemoteMediaTimeout = 20 * time.Second
)

type channelMediaSource struct {
	Data      []byte
	MediaType string
	FileName  string
	Kind      string
}

// channelMediaSource 经过本地白名单或远程 URL 读取后，才能转换成 provider
// 能力接口使用的 ChannelMedia。转换集中在这里，避免把未标记 kind 的原始媒体
// 直接传入平台适配器，导致图片/文件走错上传协议。
func (source channelMediaSource) channelMedia() ChannelMedia {
	return ChannelMedia{
		Kind:      source.Kind,
		MediaType: source.MediaType,
		FileName:  source.FileName,
		Data:      source.Data,
	}
}

// executeProviderTool 是原版 native channel tool 的统一入口。平台校验、参数校验、
// 读取权限和 Manager capability 路由集中在这里，避免把 Feishu/Weixin 分支散落到通用工具。
func (e *channelAgentToolExecutor) executeProviderTool(
	ctx context.Context,
	call services.PetAgentToolCall,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	switch call.Name {
	case channelToolFeishuSendImage:
		return e.executeFeishuSendImage(ctx, args, result, instance)
	case channelToolFeishuSendFile:
		return e.executeFeishuSendFile(ctx, args, result, instance)
	case channelToolFeishuListChatMembers:
		return e.executeFeishuListChatMembers(ctx, args, result, instance)
	case channelToolFeishuAtMember:
		return e.executeFeishuAtMember(ctx, args, result, instance)
	case channelToolFeishuSendUrgent:
		return e.executeFeishuSendUrgent(ctx, args, result, instance)
	case channelToolFeishuBitableListApps:
		return e.executeFeishuBitableListApps(ctx, args, result, instance)
	case channelToolFeishuBitableListTables:
		return e.executeFeishuBitableListTables(ctx, args, result, instance)
	case channelToolFeishuBitableListFields:
		return e.executeFeishuBitableListFields(ctx, args, result, instance)
	case channelToolFeishuBitableGetRecords:
		return e.executeFeishuBitableGetRecords(ctx, args, result, instance)
	case channelToolFeishuBitableCreate:
		return e.executeFeishuBitableWriteRecords(ctx, args, result, instance, false)
	case channelToolFeishuBitableUpdate:
		return e.executeFeishuBitableWriteRecords(ctx, args, result, instance, true)
	case channelToolFeishuBitableDelete:
		return e.executeFeishuBitableDeleteRecords(ctx, args, result, instance)
	case channelToolWeixinSendImage:
		return e.executeWeixinSendImage(ctx, args, result, instance)
	case channelToolWeixinSendFile:
		return e.executeWeixinSendFile(ctx, args, result, instance)
	default:
		return channelToolError(result, services.PetAgentToolErrorUnknownTool, "unsupported provider channel tool"), nil
	}
}

func (e *channelAgentToolExecutor) executeFeishuSendImage(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "chat_id", "file_path"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := requireProviderInstance(instance, ChannelTypeFeishu); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, chatID, sourcePath, err := providerMediaArguments(args)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelProviderToolError(result, err), nil
	}
	media, err := e.readChannelMedia(ctx, sourcePath, "image.png")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	media.Kind = "image"
	if e.manager == nil {
		return channelProviderToolError(result, errors.New("channel manager is unavailable")), nil
	}
	messageID, err := e.manager.SendFeishuImage(ctx, instance.ID, chatID, media.channelMedia())
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	return channelToolContent(result, map[string]any{"ok": true, "messageId": messageID}), nil
}

func (e *channelAgentToolExecutor) executeFeishuSendFile(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "chat_id", "file_path", "file_type"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := requireProviderInstance(instance, ChannelTypeFeishu); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, chatID, sourcePath, err := providerMediaArguments(args)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	fileType, err := optionalChannelString(args, "file_type", "")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	if fileType != "" && !isValidFeishuFileType(fileType) {
		return channelProviderToolError(result, errors.New("file_type must be opus, mp4, pdf, doc, xls, ppt, or stream")), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelProviderToolError(result, err), nil
	}
	media, err := e.readChannelMedia(ctx, sourcePath, "file")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	media.Kind = "file"
	if fileType == "" {
		fileType = inferFeishuFileType(media.FileName)
	}
	if e.manager == nil {
		return channelProviderToolError(result, errors.New("channel manager is unavailable")), nil
	}
	messageID, err := e.manager.SendFeishuFile(ctx, instance.ID, chatID, media.channelMedia(), fileType)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	return channelToolContent(result, map[string]any{"ok": true, "messageId": messageID}), nil
}

func (e *channelAgentToolExecutor) executeWeixinSendImage(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "chat_id", "file_path", "content"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := requireProviderInstance(instance, ChannelTypeWeixin); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, chatID, sourcePath, err := providerMediaArguments(args)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	caption, err := optionalChannelString(args, "content", "")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelProviderToolError(result, err), nil
	}
	media, err := e.readChannelMedia(ctx, sourcePath, "image.png")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	media.Kind = "image"
	if e.manager == nil {
		return channelProviderToolError(result, errors.New("channel manager is unavailable")), nil
	}
	messageID, err := e.manager.SendWeixinImage(ctx, instance.ID, chatID, media.channelMedia(), caption)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	return channelToolContent(result, map[string]any{"ok": true, "messageId": messageID}), nil
}

func (e *channelAgentToolExecutor) executeWeixinSendFile(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "chat_id", "file_path", "content"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := requireProviderInstance(instance, ChannelTypeWeixin); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, chatID, sourcePath, err := providerMediaArguments(args)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	caption, err := optionalChannelString(args, "content", "")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelProviderToolError(result, err), nil
	}
	media, err := e.readChannelMedia(ctx, sourcePath, "file")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	media.Kind = "file"
	if e.manager == nil {
		return channelProviderToolError(result, errors.New("channel manager is unavailable")), nil
	}
	messageID, err := e.manager.SendWeixinFile(ctx, instance.ID, chatID, media.channelMedia(), caption)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	return channelToolContent(result, map[string]any{"ok": true, "messageId": messageID}), nil
}

func (e *channelAgentToolExecutor) executeFeishuListChatMembers(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "chat_id", "page_size", "page_token", "member_id_type"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := requireProviderInstance(instance, ChannelTypeFeishu); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	chatID, err := optionalChannelString(args, "chat_id", e.chatID)
	if err != nil || chatID == "" {
		if err == nil {
			err = errors.New("chat_id is required when no current chat is available")
		}
		return channelProviderToolError(result, err), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pageSize, err := providerToolPageSize(args, "page_size", 50, 50)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	pageToken, err := optionalChannelString(args, "page_token", "")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	memberIDType, err := optionalChannelString(args, "member_id_type", "open_id")
	if err != nil || (memberIDType != "open_id" && memberIDType != "user_id" && memberIDType != "union_id") {
		if err == nil {
			err = errors.New("member_id_type must be open_id, user_id, or union_id")
		}
		return channelProviderToolError(result, err), nil
	}
	if e.manager == nil {
		return channelProviderToolError(result, errors.New("channel manager is unavailable")), nil
	}
	page, err := e.manager.ListFeishuChatMembers(ctx, instance.ID, chatID, pageSize, pageToken, memberIDType)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	return channelToolContent(result, page), nil
}

func (e *channelAgentToolExecutor) executeFeishuAtMember(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "chat_id", "user_ids", "at_all", "text"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := requireProviderInstance(instance, ChannelTypeFeishu); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	chatID, err := optionalChannelString(args, "chat_id", e.chatID)
	if err != nil || chatID == "" {
		if err == nil {
			err = errors.New("chat_id is required when no current chat is available")
		}
		return channelProviderToolError(result, err), nil
	}
	userIDs, err := providerToolStringArray(args, "user_ids", false)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	atAll, err := optionalChannelBool(args, "at_all", false)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	text, err := requiredChannelString(args, "text")
	if err != nil || (strings.TrimSpace(text) == "" && len(userIDs) == 0 && !atAll) {
		if err == nil {
			err = errors.New("mention message is empty")
		}
		return channelProviderToolError(result, err), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if e.manager == nil {
		return channelProviderToolError(result, errors.New("channel manager is unavailable")), nil
	}
	messageID, err := e.manager.AtFeishuMember(ctx, instance.ID, chatID, userIDs, atAll, text)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	return channelToolContent(result, map[string]any{"ok": true, "messageId": messageID}), nil
}

func (e *channelAgentToolExecutor) executeFeishuSendUrgent(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "message_id", "user_ids", "urgent_types"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := requireProviderInstance(instance, ChannelTypeFeishu); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	messageID, err := requiredChannelString(args, "message_id")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	userIDs, err := providerToolStringArray(args, "user_ids", true)
	if err != nil || len(userIDs) == 0 {
		if err == nil {
			err = errors.New("user_ids must not be empty")
		}
		return channelProviderToolError(result, err), nil
	}
	urgentTypes, err := providerToolStringArray(args, "urgent_types", true)
	if err != nil || len(urgentTypes) == 0 {
		if err == nil {
			err = errors.New("urgent_types must not be empty")
		}
		return channelProviderToolError(result, err), nil
	}
	for _, urgentType := range urgentTypes {
		if urgentType != "app" && urgentType != "sms" {
			return channelProviderToolError(result, fmt.Errorf("unsupported urgent type %q", urgentType)), nil
		}
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if e.manager == nil {
		return channelProviderToolError(result, errors.New("channel manager is unavailable")), nil
	}
	ok, err := e.manager.SendFeishuUrgent(ctx, instance.ID, messageID, userIDs, urgentTypes)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	return channelToolContent(result, map[string]any{"ok": ok}), nil
}

func (e *channelAgentToolExecutor) executeFeishuBitableListApps(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "page_size", "page_token"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := requireProviderInstance(instance, ChannelTypeFeishu); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pageSize, err := providerToolPageSize(args, "page_size", 50, 500)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	pageToken, err := optionalChannelString(args, "page_token", "")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	data, err := e.managerListFeishuBitableApps(ctx, instance.ID, pageSize, pageToken)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	return channelToolContent(result, data), nil
}

func (e *channelAgentToolExecutor) executeFeishuBitableListTables(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "app_token", "page_size", "page_token"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, appToken, pageSize, pageToken, err := e.bitableListArguments(args, instance, 100)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	data, err := e.manager.ListFeishuBitableTables(ctx, instance.ID, appToken, pageSize, pageToken)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	_ = pluginID
	return channelToolContent(result, data), nil
}

func (e *channelAgentToolExecutor) executeFeishuBitableListFields(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "app_token", "table_id", "page_size", "page_token"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, appToken, tableID, pageSize, pageToken, err := e.bitableTableArguments(args, instance, 200)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	data, err := e.manager.ListFeishuBitableFields(ctx, instance.ID, appToken, tableID, pageSize, pageToken)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	_ = pluginID
	return channelToolContent(result, data), nil
}

func (e *channelAgentToolExecutor) executeFeishuBitableGetRecords(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "app_token", "table_id", "filter", "page_size", "page_token"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, appToken, tableID, pageSize, pageToken, err := e.bitableTableArguments(args, instance, 50)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	filter, err := optionalChannelString(args, "filter", "")
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	data, err := e.manager.GetFeishuBitableRecords(ctx, instance.ID, appToken, tableID, pageSize, pageToken, filter)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	_ = pluginID
	return channelToolContent(result, data), nil
}

func (e *channelAgentToolExecutor) executeFeishuBitableWriteRecords(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
	update bool,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "app_token", "table_id", "records"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, appToken, tableID, err := e.bitableRequiredTableArguments(args, instance)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	records, err := providerToolRecords(args, "records")
	if err != nil || len(records) == 0 {
		if err == nil {
			err = errors.New("records must not be empty")
		}
		return channelProviderToolError(result, err), nil
	}
	var data FeishuBitableData
	if update {
		data, err = e.manager.UpdateFeishuBitableRecords(ctx, instance.ID, appToken, tableID, records)
	} else {
		data, err = e.manager.CreateFeishuBitableRecords(ctx, instance.ID, appToken, tableID, records)
	}
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	_ = pluginID
	return channelToolContent(result, data), nil
}

func (e *channelAgentToolExecutor) executeFeishuBitableDeleteRecords(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "app_token", "table_id", "record_ids"); err != nil {
		return channelProviderToolError(result, err), nil
	}
	pluginID, appToken, tableID, err := e.bitableRequiredTableArguments(args, instance)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	recordIDs, err := providerToolStringArray(args, "record_ids", true)
	if err != nil || len(recordIDs) == 0 {
		if err == nil {
			err = errors.New("record_ids must not be empty")
		}
		return channelProviderToolError(result, err), nil
	}
	data, err := e.manager.DeleteFeishuBitableRecords(ctx, instance.ID, appToken, tableID, recordIDs)
	if err != nil {
		return channelProviderToolError(result, err), nil
	}
	_ = pluginID
	return channelToolContent(result, data), nil
}

func (e *channelAgentToolExecutor) bitableListArguments(
	args map[string]json.RawMessage,
	instance ChannelInstance,
	defaultPageSize int,
) (string, string, int, string, error) {
	if err := requireProviderInstance(instance, ChannelTypeFeishu); err != nil {
		return "", "", 0, "", err
	}
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return "", "", 0, "", err
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return "", "", 0, "", err
	}
	appToken, err := requiredChannelString(args, "app_token")
	if err != nil {
		return "", "", 0, "", err
	}
	pageSize, err := providerToolPageSize(args, "page_size", defaultPageSize, 500)
	if err != nil {
		return "", "", 0, "", err
	}
	pageToken, err := optionalChannelString(args, "page_token", "")
	return pluginID, appToken, pageSize, pageToken, err
}

func (e *channelAgentToolExecutor) bitableTableArguments(
	args map[string]json.RawMessage,
	instance ChannelInstance,
	defaultPageSize int,
) (string, string, string, int, string, error) {
	pluginID, appToken, pageSize, pageToken, err := e.bitableListArguments(args, instance, defaultPageSize)
	if err != nil {
		return "", "", "", 0, "", err
	}
	tableID, err := requiredChannelString(args, "table_id")
	return pluginID, appToken, tableID, pageSize, pageToken, err
}

func (e *channelAgentToolExecutor) bitableRequiredTableArguments(
	args map[string]json.RawMessage,
	instance ChannelInstance,
) (string, string, string, error) {
	if err := requireProviderInstance(instance, ChannelTypeFeishu); err != nil {
		return "", "", "", err
	}
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return "", "", "", err
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return "", "", "", err
	}
	appToken, err := requiredChannelString(args, "app_token")
	if err != nil {
		return "", "", "", err
	}
	tableID, err := requiredChannelString(args, "table_id")
	return pluginID, appToken, tableID, err
}

func (e *channelAgentToolExecutor) managerListFeishuBitableApps(ctx context.Context, id string, pageSize int, pageToken string) (FeishuBitableData, error) {
	if e == nil || e.manager == nil {
		return nil, errors.New("channel manager is unavailable")
	}
	return e.manager.ListFeishuBitableApps(ctx, id, pageSize, pageToken)
}

func providerMediaArguments(args map[string]json.RawMessage) (string, string, string, error) {
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return "", "", "", err
	}
	chatID, err := requiredChannelString(args, "chat_id")
	if err != nil {
		return "", "", "", err
	}
	sourcePath, err := requiredChannelString(args, "file_path")
	return pluginID, chatID, sourcePath, err
}

func requireProviderInstance(instance ChannelInstance, expected string) error {
	if instance.Type != expected {
		return fmt.Errorf("tool is only available for %s", expected)
	}
	return nil
}

func channelProviderToolError(result services.PetAgentToolResult, err error) services.PetAgentToolResult {
	if err == nil {
		err = errors.New("provider channel tool failed")
	}
	return channelToolError(result, services.PetAgentToolErrorExecution, err.Error())
}

func providerToolPageSize(args map[string]json.RawMessage, key string, fallback, max int) (int, error) {
	value, err := optionalChannelInt(args, key, fallback)
	if err != nil {
		return 0, err
	}
	if value < 1 || value > max {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, max)
	}
	return value, nil
}

func providerToolStringArray(args map[string]json.RawMessage, key string, required bool) ([]string, error) {
	raw, ok := args[key]
	if !ok {
		if required {
			return nil, fmt.Errorf("%s is required", key)
		}
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result, nil
}

func providerToolRecords(args map[string]json.RawMessage, key string) ([]map[string]any, error) {
	raw, ok := args[key]
	if !ok {
		return nil, fmt.Errorf("%s is required", key)
	}
	var records []map[string]any
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("%s must be an array of objects", key)
	}
	if records == nil {
		return []map[string]any{}, nil
	}
	return records, nil
}

func (e *channelAgentToolExecutor) readChannelMedia(ctx context.Context, source, defaultName string) (channelMediaSource, error) {
	if strings.TrimSpace(source) == "" {
		return channelMediaSource{}, errors.New("file_path is required")
	}
	if e == nil || e.limits.MaxFileBytes <= 0 {
		return channelMediaSource{}, errors.New("channel media limit is unavailable")
	}
	// Windows 盘符形如 F:\\path，在 url.Parse 中会被误识别成 scheme=f；
	// 先判断绝对本地路径，避免媒体工具把正常文件路径拒绝成非法 URL。
	if !filepath.IsAbs(source) {
		parsed, err := url.Parse(source)
		if err == nil && parsed.Scheme != "" {
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return channelMediaSource{}, errors.New("file_path URL must use http or https")
			}
			return readChannelRemoteMedia(ctx, parsed, e.limits.MaxFileBytes, defaultName)
		}
	}
	path, err := e.resolveReadableMediaPath(source)
	if err != nil {
		return channelMediaSource{}, err
	}
	if err := contextErrorChannel(ctx); err != nil {
		return channelMediaSource{}, err
	}
	data, err := osReadLimited(path, e.limits.MaxFileBytes)
	if err != nil {
		return channelMediaSource{}, err
	}
	fileName := filepath.Base(path)
	if fileName == "." || fileName == "" {
		fileName = defaultName
	}
	return channelMediaSource{Data: data, MediaType: channelMediaType(data, mime.TypeByExtension(filepath.Ext(fileName))), FileName: fileName}, nil
}

func (e *channelAgentToolExecutor) resolveReadableMediaPath(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" || strings.IndexByte(input, 0) >= 0 {
		return "", errors.New("file_path is invalid")
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workspaceRoot, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("file_path is invalid")
	}
	info, err := os.Stat(filepath.Clean(absolute))
	if err != nil || info.IsDir() {
		return "", errors.New("file was not found or is a directory")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", errors.New("file_path cannot be resolved")
	}
	canonical = filepath.Clean(canonical)
	if e.allowedReadRoot(canonical) == "" {
		return "", errors.New("path is outside the channel readable roots")
	}
	return canonical, nil
}

func readChannelRemoteMedia(ctx context.Context, parsed *url.URL, limit int64, defaultName string) (channelMediaSource, error) {
	if parsed == nil || parsed.Host == "" {
		return channelMediaSource{}, errors.New("file_path URL is invalid")
	}
	requestCtx := ctx
	if requestCtx == nil {
		requestCtx = context.Background()
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return channelMediaSource{}, err
	}
	client := &http.Client{
		Timeout: channelToolRemoteMediaTimeout,
		CheckRedirect: func(next *http.Request, _ []*http.Request) error {
			if next.URL.Scheme != "http" && next.URL.Scheme != "https" {
				return errors.New("remote media redirect must use http or https")
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return channelMediaSource{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return channelMediaSource{}, fmt.Errorf("remote media request failed: %s", response.Status)
	}
	if response.ContentLength > limit {
		return channelMediaSource{}, fmt.Errorf("media exceeds the %d byte limit", limit)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return channelMediaSource{}, err
	}
	if int64(len(data)) > limit {
		return channelMediaSource{}, fmt.Errorf("media exceeds the %d byte limit", limit)
	}
	fileName := defaultName
	if candidate, pathErr := url.PathUnescape(parsed.Path); pathErr == nil {
		candidate = filepath.Base(pathpkg.Clean(candidate))
		if candidate != "." && candidate != "/" && candidate != "" {
			fileName = candidate
		}
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "" {
		mediaType = mime.TypeByExtension(filepath.Ext(fileName))
	}
	return channelMediaSource{Data: data, MediaType: channelMediaType(data, mediaType), FileName: fileName}, nil
}

func osReadLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("file could not be read")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, errors.New("file could not be read")
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds the %d byte limit", limit)
	}
	return data, nil
}

func channelMediaType(data []byte, fallback string) string {
	fallback = strings.ToLower(strings.TrimSpace(fallback))
	if fallback != "" && fallback != "application/octet-stream" && fallback != "binary/octet-stream" {
		return fallback
	}
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 4 && string(data[:4]) == "GIF8" {
		return "image/gif"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if fallback != "" {
		return fallback
	}
	return "application/octet-stream"
}

func inferFeishuFileType(fileName string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), ".")) {
	case "opus":
		return "opus"
	case "mp4":
		return "mp4"
	case "pdf":
		return "pdf"
	case "doc", "docx":
		return "doc"
	case "xls", "xlsx":
		return "xls"
	case "ppt", "pptx":
		return "ppt"
	default:
		return "stream"
	}
}

func isValidFeishuFileType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "opus", "mp4", "pdf", "doc", "xls", "ppt", "stream":
		return true
	default:
		return false
	}
}

func channelProviderToolDefinitions(instance ChannelInstance) []services.PetAgentToolDefinition {
	switch instance.Type {
	case ChannelTypeFeishu:
		return feishuProviderToolDefinitions()
	case ChannelTypeWeixin:
		return weixinProviderToolDefinitions()
	default:
		return nil
	}
}

func feishuProviderToolDefinitions() []services.PetAgentToolDefinition {
	return []services.PetAgentToolDefinition{
		{Name: channelToolFeishuSendImage, Description: "Send an image to a Feishu chat from a local path or HTTP/HTTPS URL.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "chat_id": channelStringSchema("The Feishu chat ID"), "file_path": channelStringSchema("Absolute local file path or HTTP/HTTPS URL")}, "plugin_id", "chat_id", "file_path")},
		{Name: channelToolFeishuSendFile, Description: "Send a file to a Feishu chat from a local path or HTTP/HTTPS URL.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "chat_id": channelStringSchema("The Feishu chat ID"), "file_path": channelStringSchema("Absolute local file path or HTTP/HTTPS URL"), "file_type": map[string]any{"type": "string", "enum": []string{"opus", "mp4", "pdf", "doc", "xls", "ppt", "stream"}, "description": "Optional override; otherwise infer from the file extension"}}, "plugin_id", "chat_id", "file_path")},
		{Name: channelToolFeishuListChatMembers, Description: "List members in a Feishu chat or group for @mentions.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "chat_id": channelStringSchema("Optional chat ID; defaults to the current chat"), "page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}, "page_token": channelStringSchema("Pagination token"), "member_id_type": map[string]any{"type": "string", "enum": []string{"open_id", "user_id", "union_id"}}}, "plugin_id")},
		{Name: channelToolFeishuAtMember, Description: "Mention members in a Feishu group chat.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "chat_id": channelStringSchema("Optional chat ID; defaults to the current chat"), "user_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "at_all": map[string]any{"type": "boolean"}, "text": channelStringSchema("Message text")}, "plugin_id", "text")},
		{Name: channelToolFeishuSendUrgent, Description: "Send an app or SMS urgent push for a Feishu message.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "message_id": channelStringSchema("Target Feishu message ID"), "user_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "urgent_types": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"app", "sms"}}}}, "plugin_id", "message_id", "user_ids", "urgent_types")},
		{Name: channelToolFeishuBitableListApps, Description: "List accessible Feishu Bitable apps.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "page_token": channelStringSchema("Pagination token")}, "plugin_id")},
		{Name: channelToolFeishuBitableListTables, Description: "List tables in a Feishu Bitable app.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "app_token": channelStringSchema("Bitable app token"), "page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "page_token": channelStringSchema("Pagination token")}, "plugin_id", "app_token")},
		{Name: channelToolFeishuBitableListFields, Description: "List fields in a Feishu Bitable table.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "app_token": channelStringSchema("Bitable app token"), "table_id": channelStringSchema("Bitable table ID"), "page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "page_token": channelStringSchema("Pagination token")}, "plugin_id", "app_token", "table_id")},
		{Name: channelToolFeishuBitableGetRecords, Description: "Get records from a Feishu Bitable table.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "app_token": channelStringSchema("Bitable app token"), "table_id": channelStringSchema("Bitable table ID"), "filter": channelStringSchema("Optional filter formula"), "page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "page_token": channelStringSchema("Pagination token")}, "plugin_id", "app_token", "table_id")},
		{Name: channelToolFeishuBitableCreate, Description: "Create records in a Feishu Bitable table.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "app_token": channelStringSchema("Bitable app token"), "table_id": channelStringSchema("Bitable table ID"), "records": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}, "plugin_id", "app_token", "table_id", "records")},
		{Name: channelToolFeishuBitableUpdate, Description: "Update records in a Feishu Bitable table.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "app_token": channelStringSchema("Bitable app token"), "table_id": channelStringSchema("Bitable table ID"), "records": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}, "plugin_id", "app_token", "table_id", "records")},
		{Name: channelToolFeishuBitableDelete, Description: "Delete records from a Feishu Bitable table.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The Feishu channel instance ID"), "app_token": channelStringSchema("Bitable app token"), "table_id": channelStringSchema("Bitable table ID"), "record_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "plugin_id", "app_token", "table_id", "record_ids")},
	}
}

func weixinProviderToolDefinitions() []services.PetAgentToolDefinition {
	return []services.PetAgentToolDefinition{
		{Name: channelToolWeixinSendImage, Description: "Send an image to an official Weixin chat from a local path or HTTP/HTTPS URL.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The official Weixin channel instance ID"), "chat_id": channelStringSchema("The Weixin chat ID"), "file_path": channelStringSchema("Absolute local file path or HTTP/HTTPS URL"), "content": channelStringSchema("Optional text sent before the image")}, "plugin_id", "chat_id", "file_path")},
		{Name: channelToolWeixinSendFile, Description: "Send a file to an official Weixin chat from a local path or HTTP/HTTPS URL.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("The official Weixin channel instance ID"), "chat_id": channelStringSchema("The Weixin chat ID"), "file_path": channelStringSchema("Absolute local file path or HTTP/HTTPS URL"), "content": channelStringSchema("Optional text sent before the file")}, "plugin_id", "chat_id", "file_path")},
	}
}
