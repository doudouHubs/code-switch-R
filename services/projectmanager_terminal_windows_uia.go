//go:build windows

package services

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	projectManagerUIACLSCTXInProcServer      = 0x1
	projectManagerUIACoInitApartmentThreaded = 0x2
	projectManagerUIASFalse                  = 0x1
	projectManagerUIARPCChangedMode          = 0x80010106
	projectManagerUIATreeScopeDescendants    = 0x4
	projectManagerUIAControlTypePropertyID   = 30003
	projectManagerUIATabItemControlTypeID    = 50019
	projectManagerUIAVariantI4               = 3
	projectManagerUIASelectionItemPatternID  = 10010
)

var (
	projectManagerUIAOle32DLL                   = windows.NewLazySystemDLL("ole32.dll")
	projectManagerUIAOleAut32DLL                = windows.NewLazySystemDLL("oleaut32.dll")
	projectManagerUIACoInitializeExProc         = projectManagerUIAOle32DLL.NewProc("CoInitializeEx")
	projectManagerUIACoUninitializeProc         = projectManagerUIAOle32DLL.NewProc("CoUninitialize")
	projectManagerUIACoCreateInstanceProc       = projectManagerUIAOle32DLL.NewProc("CoCreateInstance")
	projectManagerUIASafeArrayAccessDataProc    = projectManagerUIAOleAut32DLL.NewProc("SafeArrayAccessData")
	projectManagerUIASafeArrayUnaccessDataProc  = projectManagerUIAOleAut32DLL.NewProc("SafeArrayUnaccessData")
	projectManagerUIASafeArrayGetLBoundProc     = projectManagerUIAOleAut32DLL.NewProc("SafeArrayGetLBound")
	projectManagerUIASafeArrayGetUBoundProc     = projectManagerUIAOleAut32DLL.NewProc("SafeArrayGetUBound")
	projectManagerUIASafeArrayDestroyProc       = projectManagerUIAOleAut32DLL.NewProc("SafeArrayDestroy")
	projectManagerUIASysFreeStringProc          = projectManagerUIAOleAut32DLL.NewProc("SysFreeString")
	projectManagerReadTerminalTabs              = listProjectManagerTerminalTabs
	projectManagerSelectTerminalTab             = selectProjectManagerTerminalTab
	projectManagerTerminalTabRuntimeIDAvailable = projectManagerTerminalTabRuntimeIDIsAvailable
)

var (
	projectManagerUIAutomationCLSID = windows.GUID{
		Data1: 0xff48dba4,
		Data2: 0x60ef,
		Data3: 0x4201,
		Data4: [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e},
	}
	projectManagerUIAutomationIID = windows.GUID{
		Data1: 0x30cbe57d,
		Data2: 0xd9d0,
		Data3: 0x452a,
		Data4: [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee},
	}
	projectManagerUIASelectionItemPatternIID = windows.GUID{
		Data1: 0xa8efa66a,
		Data2: 0x0fda,
		Data3: 0x421a,
		Data4: [8]byte{0x91, 0x94, 0x38, 0x02, 0x1f, 0x35, 0x78, 0xea},
	}
)

var errProjectManagerTerminalTabNotFound = errors.New("未找到对应的 Windows Terminal tab")

type projectManagerTerminalTabRef struct {
	RuntimeID []int
	Title     string
}

// projectManagerUIAVariant 只承载 UI Automation 的 VT_I4 控件类型条件。
// 保持原生 VARIANT 的 16 字节布局，避免为了一个整数再引入额外的 COM 包。
type projectManagerUIAVariant struct {
	VT         uint16
	Reserved1  uint16
	Reserved2  uint16
	Reserved3  uint16
	Int64Value int64
}

func listProjectManagerTerminalTabs(hwnd windows.HWND) ([]projectManagerTerminalTabRef, error) {
	if hwnd == 0 {
		return nil, errors.New("无效的 Windows Terminal 窗口句柄")
	}

	var tabs []projectManagerTerminalTabRef
	err := projectManagerWithUIAutomation(func(automation unsafe.Pointer) error {
		windowElement, err := projectManagerUIAElementFromHandle(automation, hwnd)
		if err != nil {
			return err
		}
		defer projectManagerUIARelease(windowElement)

		condition, err := projectManagerUIACreateTabItemCondition(automation)
		if err != nil {
			return err
		}
		defer projectManagerUIARelease(condition)

		items, err := projectManagerUIAFindAll(windowElement, condition)
		if err != nil {
			return err
		}
		defer projectManagerUIARelease(items)

		length, err := projectManagerUIAElementArrayLength(items)
		if err != nil {
			return err
		}

		tabs = make([]projectManagerTerminalTabRef, 0, length)
		for index := 0; index < length; index++ {
			item, err := projectManagerUIAElementArrayElementAt(items, index)
			if err != nil {
				return err
			}

			runtimeID, runtimeIDErr := projectManagerUIAElementRuntimeID(item)
			title, titleErr := projectManagerUIAElementName(item)
			projectManagerUIARelease(item)
			if runtimeIDErr != nil {
				return runtimeIDErr
			}
			if titleErr != nil {
				return titleErr
			}

			tabs = append(tabs, projectManagerTerminalTabRef{
				RuntimeID: runtimeID,
				Title:     title,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tabs, nil
}

func selectProjectManagerTerminalTab(hwnd windows.HWND, wantedRuntimeID []int) error {
	if hwnd == 0 {
		return errors.New("无效的 Windows Terminal 窗口句柄")
	}
	if !projectManagerTerminalTabRuntimeIDIsAvailable(wantedRuntimeID) {
		return errors.New("会话缺少稳定的 Windows Terminal tab 身份")
	}

	return projectManagerWithUIAutomation(func(automation unsafe.Pointer) error {
		windowElement, err := projectManagerUIAElementFromHandle(automation, hwnd)
		if err != nil {
			return err
		}
		defer projectManagerUIARelease(windowElement)

		condition, err := projectManagerUIACreateTabItemCondition(automation)
		if err != nil {
			return err
		}
		defer projectManagerUIARelease(condition)

		items, err := projectManagerUIAFindAll(windowElement, condition)
		if err != nil {
			return err
		}
		defer projectManagerUIARelease(items)

		length, err := projectManagerUIAElementArrayLength(items)
		if err != nil {
			return err
		}
		for index := 0; index < length; index++ {
			item, err := projectManagerUIAElementArrayElementAt(items, index)
			if err != nil {
				return err
			}

			runtimeID, runtimeIDErr := projectManagerUIAElementRuntimeID(item)
			if runtimeIDErr != nil {
				projectManagerUIARelease(item)
				return runtimeIDErr
			}
			if !projectManagerTerminalTabRuntimeIDsEqual(runtimeID, wantedRuntimeID) {
				projectManagerUIARelease(item)
				continue
			}

			selectErr := projectManagerUIASelectTabItem(item)
			projectManagerUIARelease(item)
			return selectErr
		}

		return errProjectManagerTerminalTabNotFound
	})
}

func projectManagerWithUIAutomation(run func(automation unsafe.Pointer) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := projectManagerUIACoInitializeExProc.Call(0, projectManagerUIACoInitApartmentThreaded)
	shouldUninitialize := hr == 0 || uint32(hr) == projectManagerUIASFalse
	if projectManagerUIAHResultFailed(hr) && uint32(hr) != projectManagerUIARPCChangedMode {
		return projectManagerUIAError("初始化 UI Automation", hr)
	}
	if shouldUninitialize {
		defer projectManagerUIACoUninitializeProc.Call()
	}

	var automation unsafe.Pointer
	hr, _, _ = projectManagerUIACoCreateInstanceProc.Call(
		uintptr(unsafe.Pointer(&projectManagerUIAutomationCLSID)),
		0,
		projectManagerUIACLSCTXInProcServer,
		uintptr(unsafe.Pointer(&projectManagerUIAutomationIID)),
		uintptr(unsafe.Pointer(&automation)),
	)
	if projectManagerUIAHResultFailed(hr) || automation == nil {
		return projectManagerUIAError("创建 UI Automation 客户端", hr)
	}
	defer projectManagerUIARelease(automation)

	return run(automation)
}

func projectManagerUIAElementFromHandle(automation unsafe.Pointer, hwnd windows.HWND) (unsafe.Pointer, error) {
	var element unsafe.Pointer
	hr := projectManagerUIAInvoke(
		automation,
		6,
		uintptr(hwnd),
		uintptr(unsafe.Pointer(&element)),
	)
	if projectManagerUIAHResultFailed(hr) || element == nil {
		return nil, projectManagerUIAError("读取 Windows Terminal 自动化根节点", hr)
	}
	return element, nil
}

func projectManagerUIACreateTabItemCondition(automation unsafe.Pointer) (unsafe.Pointer, error) {
	value := projectManagerUIAVariant{
		VT:         projectManagerUIAVariantI4,
		Int64Value: projectManagerUIATabItemControlTypeID,
	}
	var condition unsafe.Pointer
	hr := projectManagerUIAInvoke(
		automation,
		23,
		projectManagerUIAControlTypePropertyID,
		uintptr(unsafe.Pointer(&value)),
		uintptr(unsafe.Pointer(&condition)),
	)
	if projectManagerUIAHResultFailed(hr) || condition == nil {
		return nil, projectManagerUIAError("创建 Windows Terminal tab 查询条件", hr)
	}
	return condition, nil
}

func projectManagerUIAFindAll(element unsafe.Pointer, condition unsafe.Pointer) (unsafe.Pointer, error) {
	var items unsafe.Pointer
	hr := projectManagerUIAInvoke(
		element,
		6,
		projectManagerUIATreeScopeDescendants,
		uintptr(condition),
		uintptr(unsafe.Pointer(&items)),
	)
	if projectManagerUIAHResultFailed(hr) || items == nil {
		return nil, projectManagerUIAError("枚举 Windows Terminal tab", hr)
	}
	return items, nil
}

func projectManagerUIAElementArrayLength(items unsafe.Pointer) (int, error) {
	var length int32
	hr := projectManagerUIAInvoke(items, 3, uintptr(unsafe.Pointer(&length)))
	if projectManagerUIAHResultFailed(hr) || length < 0 {
		return 0, projectManagerUIAError("读取 Windows Terminal tab 数量", hr)
	}
	return int(length), nil
}

func projectManagerUIAElementArrayElementAt(items unsafe.Pointer, index int) (unsafe.Pointer, error) {
	var element unsafe.Pointer
	hr := projectManagerUIAInvoke(
		items,
		4,
		uintptr(index),
		uintptr(unsafe.Pointer(&element)),
	)
	if projectManagerUIAHResultFailed(hr) || element == nil {
		return nil, projectManagerUIAError("读取 Windows Terminal tab", hr)
	}
	return element, nil
}

func projectManagerUIAElementRuntimeID(element unsafe.Pointer) ([]int, error) {
	var runtimeID unsafe.Pointer
	hr := projectManagerUIAInvoke(element, 4, uintptr(unsafe.Pointer(&runtimeID)))
	if projectManagerUIAHResultFailed(hr) || runtimeID == nil {
		return nil, projectManagerUIAError("读取 Windows Terminal tab 身份", hr)
	}
	defer projectManagerUIASafeArrayDestroyProc.Call(uintptr(runtimeID))

	return projectManagerUIACopyRuntimeID(runtimeID)
}

func projectManagerUIACopyRuntimeID(runtimeID unsafe.Pointer) ([]int, error) {
	var lowerBound int32
	hr, _, _ := projectManagerUIASafeArrayGetLBoundProc.Call(
		uintptr(runtimeID),
		1,
		uintptr(unsafe.Pointer(&lowerBound)),
	)
	if projectManagerUIAHResultFailed(hr) {
		return nil, projectManagerUIAError("读取 Windows Terminal tab 身份下界", hr)
	}

	var upperBound int32
	hr, _, _ = projectManagerUIASafeArrayGetUBoundProc.Call(
		uintptr(runtimeID),
		1,
		uintptr(unsafe.Pointer(&upperBound)),
	)
	if projectManagerUIAHResultFailed(hr) {
		return nil, projectManagerUIAError("读取 Windows Terminal tab 身份上界", hr)
	}
	if upperBound < lowerBound {
		return []int{}, nil
	}

	var data unsafe.Pointer
	hr, _, _ = projectManagerUIASafeArrayAccessDataProc.Call(
		uintptr(runtimeID),
		uintptr(unsafe.Pointer(&data)),
	)
	if projectManagerUIAHResultFailed(hr) || data == nil {
		return nil, projectManagerUIAError("读取 Windows Terminal tab 身份数据", hr)
	}
	defer projectManagerUIASafeArrayUnaccessDataProc.Call(uintptr(runtimeID))

	count := int(upperBound-lowerBound) + 1
	values := unsafe.Slice((*int32)(data), count)
	result := make([]int, count)
	for index, value := range values {
		result[index] = int(value)
	}
	return result, nil
}

func projectManagerUIAElementName(element unsafe.Pointer) (string, error) {
	var value unsafe.Pointer
	hr := projectManagerUIAInvoke(element, 23, uintptr(unsafe.Pointer(&value)))
	if projectManagerUIAHResultFailed(hr) {
		return "", projectManagerUIAError("读取 Windows Terminal tab 标题", hr)
	}
	if value == nil {
		return "", nil
	}
	defer projectManagerUIASysFreeStringProc.Call(uintptr(value))

	return windows.UTF16PtrToString((*uint16)(value)), nil
}

func projectManagerUIASelectTabItem(element unsafe.Pointer) error {
	var selectionItem unsafe.Pointer
	hr := projectManagerUIAInvoke(
		element,
		14,
		projectManagerUIASelectionItemPatternID,
		uintptr(unsafe.Pointer(&projectManagerUIASelectionItemPatternIID)),
		uintptr(unsafe.Pointer(&selectionItem)),
	)
	if projectManagerUIAHResultFailed(hr) || selectionItem == nil {
		return projectManagerUIAError("获取 Windows Terminal tab 选择能力", hr)
	}
	defer projectManagerUIARelease(selectionItem)

	hr = projectManagerUIAInvoke(selectionItem, 3)
	if projectManagerUIAHResultFailed(hr) {
		return projectManagerUIAError("选中 Windows Terminal tab", hr)
	}
	return nil
}

func projectManagerUIAInvoke(instance unsafe.Pointer, methodIndex uintptr, arguments ...uintptr) uintptr {
	if instance == nil {
		return ^uintptr(0)
	}
	vtable := *(*unsafe.Pointer)(instance)
	method := *(*uintptr)(unsafe.Add(vtable, methodIndex*unsafe.Sizeof(uintptr(0))))
	args := make([]uintptr, 0, len(arguments)+1)
	args = append(args, uintptr(instance))
	args = append(args, arguments...)
	hr, _, _ := syscall.SyscallN(method, args...)
	runtime.KeepAlive(instance)
	return hr
}

func projectManagerUIARelease(instance unsafe.Pointer) {
	if instance == nil {
		return
	}
	_ = projectManagerUIAInvoke(instance, 2)
}

func projectManagerUIAHResultFailed(hr uintptr) bool {
	return int32(uint32(hr)) < 0
}

func projectManagerUIAError(action string, hr uintptr) error {
	if hr == 0 {
		return fmt.Errorf("%s失败", action)
	}
	return fmt.Errorf("%s失败: HRESULT=0x%08x", action, uint32(hr))
}

func projectManagerTerminalTabRuntimeIDIsAvailable(runtimeID []int) bool {
	return len(runtimeID) > 0
}

func projectManagerTerminalTabRuntimeIDsEqual(left []int, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func projectManagerTerminalTabRuntimeIDKey(runtimeID []int) string {
	if len(runtimeID) == 0 {
		return ""
	}

	parts := make([]string, 0, len(runtimeID))
	for _, value := range runtimeID {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ":")
}
