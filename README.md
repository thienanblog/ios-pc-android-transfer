# PhotoBridge

Chuyển ảnh và video giữa **iPhone, máy tính và Android** qua Wi-Fi, ngay trong trình duyệt.

- **iPhone → máy tính:** giữ nguyên file gốc (HEIC, HEVC/MOV), không nén, không đổi định dạng.
- **Máy tính → Android (hoặc máy bất kỳ):** nếu máy nhận không đọc được HEIC hay HEVC, PhotoBridge tự đổi sang **JPG/PNG** và **MP4 (H.264)** trước khi tải về. Máy đọc được thì nhận file gốc.
- Chỉ chạy trên máy tính của bạn, trong mạng nội bộ. Không có máy chủ trên internet, không cần tài khoản, điện thoại không cần cài app.

Máy tính chạy một chương trình nhỏ viết bằng Go (một file duy nhất). Điện thoại quét mã QR là vào được trang web để gửi và nhận file.

*English summary at the end.*

## Cách dùng nhanh

1. Tải bản phù hợp ở mục [Releases](../../releases):
   - Windows: `photobridge-…-windows-amd64.zip` (máy ARM như Surface Pro X thì lấy `windows-arm64`)
   - macOS: `photobridge-…-darwin-arm64.zip` cho máy Apple Silicon (M1 trở lên), `darwin-amd64` cho máy Intel
   - Linux: `photobridge-…-linux-amd64.tar.gz`
2. Giải nén và chạy `photobridge` (Windows: bấm đúp `photobridge.exe`).
3. Trình duyệt trên máy tính tự mở trang PhotoBridge với một **mã QR**. Mã QR cũng hiện ngay trong cửa sổ dòng lệnh.
4. Điện thoại kết nối **cùng mạng Wi-Fi** với máy tính, mở camera quét mã QR.
5. Trên điện thoại:
   - Bấm **Chọn ảnh hoặc video** để gửi lên máy tính.
   - Ở mục **Ảnh và video trên máy tính**, bấm **Tải về** để lấy file từ máy tính về điện thoại.

File nhận được nằm trong thư mục `PhotoBridge` ở thư mục người dùng (ví dụ `C:\Users\<tên>\PhotoBridge` hoặc `~/PhotoBridge`). Muốn gửi file từ máy tính sang điện thoại, kéo thả file vào trang web trên máy tính hoặc chép thẳng vào thư mục này.

### Lần đầu mở trên Windows và macOS

- **Windows:** SmartScreen có thể báo "Windows protected your PC". Bấm **More info → Run anyway**. Khi tường lửa hỏi, cho phép **Private networks** (mạng riêng) để điện thoại kết nối được.
- **macOS:** vì chương trình chưa được Apple ký, lần đầu hãy bấm chuột phải vào `photobridge` → **Open**, hoặc chạy trong Terminal:

  ```sh
  xattr -d com.apple.quarantine ./photobridge
  ./photobridge
  ```

## Cài công cụ đổi định dạng (khuyên dùng)

Gửi và nhận **file gốc** luôn dùng được, không cần cài gì thêm. Để **đổi** HEIC sang JPG/PNG và HEVC sang MP4, PhotoBridge dùng các công cụ có sẵn trên máy. Nó tự tìm khi khởi động và báo trong cửa sổ dòng lệnh cũng như trên trang web.

| Hệ điều hành | Ảnh HEIC → JPG/PNG | Video HEVC → MP4 |
|---|---|---|
| Windows | `winget install Gyan.FFmpeg` (ffmpeg 7.1 trở lên đọc được HEIC) hoặc `winget install ImageMagick.ImageMagick` | `winget install Gyan.FFmpeg` |
| macOS | Có sẵn (`sips` của macOS) | `brew install ffmpeg` |
| Ubuntu/Debian | `sudo apt install libvips-tools` hoặc `sudo apt install libheif-examples libheif-plugin-libde265` | `sudo apt install ffmpeg` |

Sau khi cài, tắt PhotoBridge và mở lại.

Không muốn cài vào hệ thống? Đặt `ffmpeg.exe` và `ffprobe.exe` (Windows) hoặc `ffmpeg`, `ffprobe` cạnh file `photobridge`, hoặc trong thư mục con `bin/` hay `ffmpeg/bin/`. PhotoBridge sẽ tìm thấy.

Lưu ý: ffmpeg trên Ubuntu 24.04 là bản 6.1, chưa đọc được HEIC, nên trên Linux hãy cài thêm `libvips-tools` hoặc `libheif-examples` cho ảnh.

PhotoBridge thử lần lượt: `sips` (macOS), libvips, ImageMagick, libheif (`heif-convert`), rồi ffmpeg 7.1+. Video quay HDR trên iPhone (HLG, Dolby Vision) được chuyển màu về SDR khi ffmpeg có bộ lọc `zscale` (các bản ffmpeg phổ biến đều có), để màu không bị nhạt.

## Giữ file gốc khi gửi từ iPhone

Đây là phần iOS quyết định, không phải trang web:

- PhotoBridge ghi **đúng từng byte** mà trình duyệt gửi lên. Sau khi gửi, mỗi file có nhãn cho biết máy tính nhận được gì: **HEIC gốc**, **HEVC**, hay **JPEG / H.264**.
- Trang web không giới hạn loại file khi chọn (không dùng thuộc tính `accept`), vì giới hạn đó làm Safari đổi định dạng ảnh ([WebKit bug 303803](https://bugs.webkit.org/show_bug.cgi?id=303803)).
- Khi chọn từ thư viện Ảnh, tùy phiên bản iOS, iPhone **có thể vẫn tự đổi** HEIC sang JPEG hoặc nén video trước khi đưa cho trình duyệt. Nếu thấy nhãn **JPEG** cho ảnh chụp bằng iPhone, đó là iOS đã đổi.
- **Cách chắc chắn giữ gốc:** trong app Ảnh, chọn ảnh/video → Chia sẻ → **Lưu vào Tệp**. Sau đó trên trang PhotoBridge bấm **Chọn ảnh hoặc video** → **Chọn tệp** và lấy file từ app Tệp. File lấy từ app Tệp được gửi nguyên vẹn.
- Giữ màn hình sáng cho tới khi gửi xong. Nếu Wi-Fi chập chờn hoặc trang bị tải lại, chọn lại đúng file đó, PhotoBridge sẽ gửi tiếp từ chỗ đã dừng.

Live Photo: trình duyệt chỉ nhận phần ảnh tĩnh, không có đoạn video đi kèm.

## Tải về điện thoại

Ở ô **Khi tải về máy này**:

- **Tự đổi định dạng nếu máy này không đọc được** (mặc định): trang web thử mở một ảnh HEIC nhỏ và hỏi trình duyệt có phát được HEVC không. Không được thì đổi sang JPG/PNG (chọn ở ô **Đổi ảnh sang**) và MP4 H.264.
- **Luôn tải file gốc.**
- **Luôn đổi sang JPG/PNG và MP4 (H.264).**

Chrome trên Android hầu như không mở được HEIC, nên ở chế độ tự động ảnh HEIC sẽ được đổi sang JPG, dù app Thư viện của nhiều máy Android đời mới đọc được HEIC. Nếu máy bạn đọc được HEIC, chọn **Luôn tải file gốc**.

Video dài cần thời gian đổi; trang web hiện phần trăm tiến độ. File đã đổi được giữ tạm 3 ngày nên lần tải sau nhanh hơn.

Trên iPhone, file tải về nằm trong app Tệp → Tải về. Để đưa ảnh vào app Ảnh, bấm **Xem**, giữ ngón tay trên ảnh rồi chọn **Lưu vào Ảnh**.

## Tuỳ chọn dòng lệnh

```
photobridge [-dir THƯ_MỤC] [-port 8686] [-no-browser] [-new-key] [-version]
```

- `-dir`: thư mục lưu và chia sẻ file (mặc định `~/PhotoBridge`).
- `-port`: cổng web (mặc định 8686; nếu bận thì dùng cổng kế tiếp).
- `-no-browser`: không tự mở trình duyệt.
- `-new-key`: tạo mã truy cập mới. Điện thoại đã quét trước đó sẽ phải quét lại.

## Bảo mật

- Chỉ ai có **mã truy cập** (nằm trong mã QR) mới vào được. Điện thoại nhớ mã này trong cookie, lần sau không cần quét lại. Muốn thu hồi, chạy với `-new-key`.
- Trang trên chính máy tính (`localhost`) không cần mã.
- Dữ liệu đi trong mạng nội bộ bằng HTTP thường, không mã hoá. Đừng dùng trên Wi-Fi công cộng mà bạn không tin tưởng.
- Thư mục `.photobridge` trong thư mục lưu chứa mã truy cập, phần upload dở và file đã đổi tạm thời.

## Tự build từ mã nguồn

Cần Go 1.22 trở lên.

```sh
go test ./...
go run .                      # chạy thử
scripts/build.sh v0.1.0       # build cho Windows, macOS, Linux vào thư mục dist/
```

Chương trình không dùng cgo, nên build chéo cho mọi hệ điều hành từ một máy. Đẩy một tag dạng `v*` lên GitHub thì GitHub Actions tự build và tạo Release.

Cấu trúc:

- `main.go`: khởi động, tìm địa chỉ mạng, in mã QR, mở trình duyệt.
- `internal/server`: API (upload theo từng phần có thể nối tiếp, danh sách file, tải về, đổi định dạng chạy nền, ảnh thu nhỏ) và kiểm soát truy cập.
- `internal/media`: nhận diện định dạng thật của file (HEIC, HEVC, H.264 …) từ nội dung, gọi công cụ đổi định dạng.
- `internal/netutil`: chọn địa chỉ Wi-Fi/LAN phù hợp nhất.
- `web/static`: trang web (HTML, CSS, JavaScript thuần, được nhúng vào file chạy).

## Giới hạn đã biết

- Việc iOS có đổi HEIC/HEVC khi chọn từ thư viện Ảnh hay không phụ thuộc phiên bản iOS. Xem phần "Giữ file gốc khi gửi từ iPhone".
- Không dùng HTTPS nên trình duyệt không cho dùng nút Chia sẻ hệ thống (Web Share) để lưu thẳng vào thư viện ảnh.
- Live Photo chỉ gửi được phần ảnh tĩnh.

---

## English summary

PhotoBridge is a single cgo-free Go binary that serves a web page on your LAN. Phones scan a QR code to open it. iPhone uploads are stored byte-for-byte (HEIC/HEVC originals, resumable chunked upload). When a device downloads a file its browser cannot open, PhotoBridge converts HEIC to JPG/PNG and HEVC to H.264 MP4 using whatever is installed: macOS `sips`, libvips, ImageMagick, libheif or ffmpeg 7.1+ (with HDR tone-mapping). Access is protected by a key carried in the QR link. Run `go test ./...` to test and `scripts/build.sh` to cross-compile.

License: MIT.
