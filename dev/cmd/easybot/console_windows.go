//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func enableConsoleColors() bool {
	handle := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if windows.GetConsoleMode(handle, &mode) != nil {
		return false
	}
	return windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}

func waitAfterFailure() {
	var processes [2]uint32
	getProcesses := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	count, _, _ := getProcesses.Call(uintptr(unsafe.Pointer(&processes[0])), uintptr(len(processes)))
	// Explorer 双击创建的独立控制台只有当前程序；已有终端和无控制台任务直接返回错误码。
	if count != 1 {
		return
	}
	input, err := os.OpenFile("CONIN$", os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer input.Close()
	fmt.Print("\n启动失败，错误原因见上方。按回车键关闭窗口...")
	_, _ = bufio.NewReader(input).ReadString('\n')
}
