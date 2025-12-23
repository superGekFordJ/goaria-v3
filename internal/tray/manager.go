package tray

import (
	"bytes"
	"image"
	"image/png"
	"sync"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// 1. 保持原有的类型定义不变
type TrayState int

const (
	StateIdle TrayState = iota
	StateActive
	StatePaused
	StateError
)

// 缓存：防止每次更新图标都重新渲染 SVG，节省 CPU
var (
	iconCache  = make(map[TrayState][]byte)
	cacheMutex sync.Mutex
)

// 2. 保持原有的函数签名不变
// 输入：状态枚举
// 输出：[]byte (这是 PNG 格式的字节流，Wails 能够直接使用)
func GetIconForState(state TrayState) []byte {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	// 命中缓存直接返回
	if data, ok := iconCache[state]; ok {
		return data
	}

	// 根据状态选择 icons.go 里的 SVG 源码
	var svgSource []byte
	switch state {
	case StateActive:
		svgSource = IconActive
	case StatePaused:
		svgSource = IconPaused
	case StateError:
		svgSource = IconError
	default:
		svgSource = IconIdle
	}

	// 核心逻辑：将 SVG 源码渲染为 PNG 字节流
	// 32x32 是托盘图标的最佳尺寸
	pngData := renderSvgToPng(svgSource, 32, 32)

	// 写入缓存
	if pngData != nil {
		iconCache[state] = pngData
	}

	return pngData
}

// 内部辅助函数：SVG -> PNG 转换器
func renderSvgToPng(svgBytes []byte, w, h int) []byte {
	// 解析 SVG 字符串
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svgBytes))
	if err != nil {
		// 如果 SVG 格式有误，返回 nil (实际开发中建议打日志)
		return nil
	}

	// 设置目标尺寸 (32x32)
	icon.SetTarget(0, 0, float64(w), float64(h))

	// 创建画布
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	// 创建渲染器
	scanner := rasterx.NewScannerGV(w, h, img, img.Bounds())
	raster := rasterx.NewDasher(w, h, scanner)

	// 绘制
	icon.Draw(raster, 1.0)

	// 编码为 PNG 格式
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}

	return buf.Bytes()
}
