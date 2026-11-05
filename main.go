// PhotoBridge runs on your computer and lets phones on the same Wi-Fi send
// and receive photos and videos through the browser, keeping iPhone originals
// (HEIC, HEVC) and converting them for devices that cannot open them.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/thienanblog/ios-pc-android-transfer/internal/media"
	"github.com/thienanblog/ios-pc-android-transfer/internal/netutil"
	"github.com/thienanblog/ios-pc-android-transfer/internal/server"
	"github.com/thienanblog/ios-pc-android-transfer/web"
)

// version is set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	home, _ := os.UserHomeDir()
	defaultDir := filepath.Join(home, "PhotoBridge")

	dataDir := flag.String("dir", defaultDir, "thư mục lưu file nhận được / folder for received and shared files")
	port := flag.Int("port", 8686, "cổng web / web port")
	noBrowser := flag.Bool("no-browser", false, "không tự mở trình duyệt / do not open the browser")
	newToken := flag.Bool("new-key", false, "tạo mã truy cập mới (điện thoại cũ phải quét lại) / make a new access key")
	showVersion := flag.Bool("version", false, "in phiên bản / print version")
	flag.Parse()

	if *showVersion {
		fmt.Println("PhotoBridge", version)
		return
	}
	if err := run(*dataDir, *port, !*noBrowser, *newToken); err != nil {
		log.Println("Lỗi:", err)
		if runtime.GOOS == "windows" {
			// Keep the console window open long enough to read the error.
			fmt.Println("Nhấn Enter để thoát…")
			fmt.Scanln()
		}
		os.Exit(1)
	}
}

func run(dataDir string, port int, openBrowser, newToken bool) error {
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	token, err := loadToken(dataDir, newToken)
	if err != nil {
		return err
	}

	var extra []string
	if exe, err := os.Executable(); err == nil {
		extra = append(extra, filepath.Dir(exe))
	}
	tools := media.DetectTools(extra...)

	ln, actualPort, err := listen(port)
	if err != nil {
		return err
	}

	urls := func() []string {
		var out []string
		for _, c := range netutil.LANAddresses() {
			out = append(out, fmt.Sprintf("http://%s/?t=%s", net.JoinHostPort(c.IP, strconv.Itoa(actualPort)), token))
		}
		return out
	}

	srv, err := server.New(server.Config{
		DataDir: dataDir,
		Token:   token,
		Tools:   tools,
		Version: version,
		Web:     web.Files(),
		URLs:    urls,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	printBanner(dataDir, actualPort, urls(), tools)

	local := fmt.Sprintf("http://localhost:%d/", actualPort)
	if openBrowser {
		go func() {
			time.Sleep(400 * time.Millisecond)
			if err := openURL(local); err != nil {
				fmt.Println("Mở trình duyệt và vào", local)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case <-ctx.Done():
		fmt.Println("\nĐang tắt PhotoBridge…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// listen tries the wanted port, then the next few, then any free port.
func listen(port int) (net.Listener, int, error) {
	var lastErr error
	for p := port; p < port+10; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, 0, fmt.Errorf("không mở được cổng mạng: %v", lastErr)
	}
	return ln, ln.Addr().(*net.TCPAddr).Port, nil
}

// loadToken keeps the access key between runs so phones that scanned once
// stay connected.
func loadToken(dataDir string, fresh bool) (string, error) {
	dir := filepath.Join(dataDir, ".photobridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "key")
	if !fresh {
		if b, err := os.ReadFile(p); err == nil {
			if t := strings.TrimSpace(string(b)); len(t) >= 16 {
				return t, nil
			}
		}
	}
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := base64.RawURLEncoding.EncodeToString(buf)
	return t, os.WriteFile(p, []byte(t+"\n"), 0o600)
}

func printBanner(dataDir string, port int, urls []string, tools media.Tools) {
	fmt.Println()
	fmt.Println("  PhotoBridge", version)
	fmt.Println("  Thư mục lưu file:", dataDir)
	fmt.Printf("  Trên máy tính, mở: http://localhost:%d/\n", port)
	if len(urls) == 0 {
		fmt.Println("  Chưa thấy mạng Wi-Fi/LAN. Hãy kết nối mạng rồi mở lại.")
	} else {
		fmt.Println("  Trên điện thoại, quét mã QR bên dưới (hoặc mã trên trang web):")
		if q, err := qrcode.New(urls[0], qrcode.Low); err == nil {
			fmt.Println(q.ToSmallString(false))
		}
		fmt.Println("  Địa chỉ:", strings.SplitN(urls[0], "?", 2)[0])
	}
	fmt.Println()
	status := func(ok bool) string {
		if ok {
			return "có"
		}
		return "CHƯA CÀI"
	}
	fmt.Printf("  Đổi ảnh HEIC → JPG/PNG: %s\n", status(tools.CanConvertImages()))
	fmt.Printf("  Đổi video HEVC → MP4 H.264 (ffmpeg): %s\n", status(tools.CanConvertVideos()))
	if !tools.CanConvertImages() || !tools.CanConvertVideos() {
		fmt.Println("  Xem README để cài ffmpeg. Gửi và nhận file gốc vẫn dùng được.")
	}
	fmt.Println()
	fmt.Println("  Nhấn Ctrl+C để tắt.")
	fmt.Println()
}

func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}
