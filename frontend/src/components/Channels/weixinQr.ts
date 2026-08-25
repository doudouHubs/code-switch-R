import QRCode from 'qrcode'

const HTTP_URL_PATTERN = /^https?:\/\//i
const IMAGE_DATA_URL_PATTERN = /^data:image\//i

// 微信接口返回的是一个由微信页面生成二维码的登录 URL，不是可直接交给 img 的图片地址。
// 原版通过隐藏窗口截图；当前 Vue 页面直接把同一个 URL 编码成 PNG，保持扫码目标一致。
export async function createWeixinQrDataUrl(value: string): Promise<string> {
  const source = value.trim()
  if (!source) throw new Error('Weixin QR content is empty')
  if (IMAGE_DATA_URL_PATTERN.test(source)) return source
  if (!HTTP_URL_PATTERN.test(source)) throw new Error('Weixin QR content is not renderable')

  return QRCode.toDataURL(source, {
    errorCorrectionLevel: 'M',
    margin: 2,
    width: 420,
    color: {
      dark: '#111827',
      light: '#ffffff',
    },
  })
}
