/*
 _     __     _ __
| |  / /___ _(_) /____
| | /| / / __ `/ / / ___/
| |/ |/ / /_/ / / (__  )
|__/|__/\__,_/_/_/____/
The electron alternative for Go
(c) Lea Anthony 2019-present
*/

import { nanoid } from './nanoid.js';

const runtimeURL = window.location.origin + "/wails/runtime";

// Object Names
export const objectNames = Object.freeze({
    Call: 0,
    Clipboard: 1,
    Application: 2,
    Events: 3,
    ContextMenu: 4,
    Dialog: 5,
    Window: 6,
    Screens: 7,
    System: 8,
    Browser: 9,
    CancelCall: 10,
});
export let clientId = nanoid();

/**
 * Wails 的绑定调用依赖宿主 WebView 注入的消息桥；普通 Chrome/Vite 预览虽然
 * 也能加载这套 runtime 脚本，但没有原生桥，继续请求 /wails/runtime 只会让业务
 * 调用悬挂或得到无意义的 HTML 响应。因此所有需要原生能力的模块统一复用这个
 * 判定，在适配层尽早切换到各自的预览 fallback。
 */
export function isWailsRuntimeAvailable(): boolean {
    try {
        return Boolean(
            (window as any).chrome?.webview?.postMessage ||
            (window as any).webkit?.messageHandlers?.['external']?.postMessage
        );
    } catch (_) {
        return false;
    }
}

/**
 * Creates a new runtime caller with specified ID.
 *
 * @param object - The object to invoke the method on.
 * @param windowName - The name of the window.
 * @return The new runtime caller function.
 */
export function newRuntimeCaller(object: number, windowName: string = '') {
    return function (method: number, args: any = null) {
        return runtimeCallWithID(object, method, windowName, args);
    };
}

async function runtimeCallWithID(objectID: number, method: number, windowName: string, args: any): Promise<any> {
    let url = new URL(runtimeURL);
    url.searchParams.append("object", objectID.toString());
    url.searchParams.append("method", method.toString());
    if (args) { url.searchParams.append("args", JSON.stringify(args)); }

    let headers: Record<string, string> = {
        ["x-wails-client-id"]: clientId
    }
    if (windowName) {
        headers["x-wails-window-name"] = windowName;
    }

    let response = await fetch(url, { headers });
    if (!response.ok) {
        throw new Error(await response.text());
    }

    if ((response.headers.get("Content-Type")?.indexOf("application/json") ?? -1) !== -1) {
        return response.json();
    } else {
        return response.text();
    }
}
