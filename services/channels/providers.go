package channels

func RegisterBuiltinFactories(manager *Manager) {
	manager.RegisterFactory(ChannelTypeFeishu, newFeishuProvider)
	manager.RegisterFactory(ChannelTypeDingTalk, newDingTalkProvider)
	manager.RegisterFactory(ChannelTypeWeCom, newWeComProvider)
	manager.RegisterFactory(ChannelTypeQQ, newQQProvider)
	manager.RegisterFactory(ChannelTypeWeixin, newWeixinProvider)
	manager.RegisterFactory(ChannelTypeTelegram, newTelegramProvider)
	manager.RegisterFactory(ChannelTypeDiscord, newDiscordProvider)
	manager.RegisterFactory(ChannelTypeWhatsApp, newWhatsAppProvider)
}
