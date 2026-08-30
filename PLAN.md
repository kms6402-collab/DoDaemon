# DoDaemon 개발 계획서
### 3CDaemon 스타일 통합 네트워크 서버 데몬 (FTP + TFTP + Syslog)

---

## 1. 제품 개요

**DoDaemon**은 3Com의 3CDaemon을 계승하는 현대적 통합 네트워크 서버 유틸리티다. 하나의 프로세스에서 **FTP 서버, TFTP 서버, Syslog 서버**를 동시에 구동하며, 네트워크 장비(스위치, 라우터, AP 등) 관리자가 별도 설치 과정 없이 실행 파일 하나만으로 즉시 사용할 수 있는 것을 목표로 한다.

**핵심 설계 원칙**

| 원칙 | 내용 |
|---|---|
| 단일 바이너리 배포 | 외부 런타임/DLL 의존성 없음. `CGO_ENABLED=0`으로 순수 Go만 사용해 크로스 컴파일 보장 |
| 리소스 임베드 | `embed.FS`로 웹 UI 정적 자산, 기본 설정 템플릿, 라이선스 고지 등을 바이너리에 내장 |
| 즉시 실행 | 설치 프로그램 불필요. 실행 파일 더블클릭 또는 CLI 인자만으로 동작 |
| 주 타깃 Windows | 네트워크 장비 관리자의 데스크톱/서버 환경 (Windows 10/11, Server 2016+) |
| 부가 타깃 Linux/macOS | 헤드리스 서버, 컨테이너, CI 환경에서의 자동화된 펌웨어 배포 등 |

**차별점**: 원조 3CDaemon 대비 (1) TLS/FTPS·Syslog over TLS 지원, (2) 구조화 로깅과 웹 기반 실시간 모니터링, (3) YAML/TOML 기반 버전관리 가능한 설정, (4) Windows Service/systemd 네이티브 등록을 제공한다.

---

## 2. 요구사항 정의

### 2.1 FTP 서버

- **전송 모드**: 능동(PORT)/수동(PASV, EPSV) 모드 모두 지원. 수동 모드 포트 범위 설정 가능 (NAT/방화벽 환경 대응).
- **인증**: 가상 사용자 계정(설정 파일 기반, 비밀번호는 bcrypt 해시 저장), 익명(anonymous) 접속 옵션(기본 비활성화).
- **권한 제어**: 사용자/그룹별 홈 디렉터리 chroot, 디렉터리별 읽기/쓰기/삭제/목록 권한 매트릭스.
- **TLS(FTPS)**: Explicit FTPS(AUTH TLS, RFC 4217) 필수 지원. Implicit FTPS는 레거시 장비 호환을 위한 선택 옵션.
- **기타**: 동시 세션 수 제한, 사용자별 대역폭 제한(선택), UTF-8 파일명 지원.

### 2.2 TFTP 서버

- **기본**: RFC 1350 (RRQ/WRQ, netascii/octet 모드).
- **옵션 확장(RFC 2347/2348/2349)**: `blksize`(기본 512, 최대 65464까지 협상), `timeout`, `tsize` 지원 — 대용량 펌웨어 파일 전송 속도 개선에 필수.
- **시나리오 대응**: 장비 펌웨어 업로드/다운로드(WRQ로 장비→서버 config 백업, RRQ로 서버→장비 펌웨어 푸시), 다수 장비의 동시 부팅 시 TFTP 폭주 상황에서도 안정적 처리(세션별 goroutine, 재전송/타임아웃 관리).
- **경로 매핑**: TFTP는 프로토콜상 인증이 없으므로 루트 디렉터리를 명시적으로 지정하고 그 외 경로 접근을 원천 차단.

### 2.3 Syslog 서버

- **포맷 파싱**: RFC 3164(BSD syslog, 레거시 장비 다수가 여전히 사용) 및 RFC 5424(구조화 데이터 지원) 모두 파싱. 두 포맷이 혼재하는 환경을 자동 판별.
- **전송**: UDP 514(기본, 비신뢰), TCP(신뢰성 있는 대량 로그), TLS(RFC 5425, syslog 암호화 전송).
- **저장/운영**: 파일 로테이션(크기/일 단위, 오래된 로그 압축·삭제 정책), 소스 IP/호스트명/facility/severity 기준 필터링 및 검색.
- **UI 연동**: 웹 UI에서 실시간 tail, 필터 쿼리, CSV/JSON 내보내기.

### 2.4 공통 요구사항

- **접근 제어**: 서버별 IP 화이트리스트/블랙리스트(CIDR 지원).
- **로깅/감사**: 모든 서버의 연결/인증/파일 전송 이벤트를 구조화 로그(`log/slog` JSON)로 기록, 감사 추적 가능.
- **설정 파일**: YAML(기본) 지원, 스키마 검증 및 기본값 안전성 보장. TOML은 우선순위 낮은 선택지로 검토(§5 참조).
- **서비스 등록**: Windows Service(`sc create` 또는 내장 설치 커맨드), Linux systemd unit 파일 자동 생성.

### 2.5 비기능 요구사항

- 유휴 상태 메모리 사용량 30MB 이하(목표치), CPU 유휴 시 0%에 수렴.
- 설정 변경 시 서비스 재시작 없이 핫리로드(§4.4).
- 동시 TFTP 세션 수백 개, FTP 동시 연결 수십~수백, Syslog 초당 수천 메시지 수신 처리 목표.
- Go 1.22+ 기준, `go vet`/`staticcheck`/`golangci-lint` 통과, 유닛 테스트 커버리지 70% 이상(핵심 프로토콜 파서·경로 검증 로직은 90%+).

---

## 3. 범위 및 우선순위

| 단계 | 범위 |
|---|---|
| **MVP (v0.1)** | TFTP 서버(RRQ/WRQ, 옵션 협상) + FTP 서버(수동 모드, 로컬 계정 인증) + Syslog(UDP, RFC3164/5424 파싱) + YAML 설정 + CLI 실행 + 기본 콘솔 로그 |
| **v0.2** | 웹 UI(대시보드, 실시간 로그 뷰어, 설정 편집), FTP 능동 모드·FTPS, Syslog TCP/TLS, IP 화이트리스트 |
| **v0.3** | Windows Service/systemd 등록 커맨드, 설정 핫리로드, 로그 로테이션, 다국어(한/영) UI |
| **v1.0** | 코드 서명 배포, 크로스 컴파일 릴리스 파이프라인, 사용자 문서, 안정화 |

---

## 4. 시스템 아키텍처

### 4.1 전체 구조

```
                        ┌─────────────────────┐
                        │   cmd/dodaemon (main) │
                        │  CLI 파싱, 부트스트랩   │
                        └──────────┬───────────┘
                                   │
                        ┌──────────▼───────────┐
                        │   internal/config     │  YAML 로드/검증/핫리로드(fsnotify)
                        └──────────┬───────────┘
                                   │ config.Config
              ┌────────────────────┼────────────────────┐
              │                    │                     │
   ┌──────────▼─────────┐ ┌────────▼────────┐  ┌─────────▼─────────┐
   │  internal/ftp        │ │ internal/tftp   │  │ internal/syslog    │
   │  goroutine + ctx      │ │ goroutine + ctx │  │ goroutine + ctx    │
   └──────────┬─────────┘ └────────┬────────┘  └─────────┬─────────┘
              │                    │                     │
              └────────────────────┼─────────────────────┘
                                   │ 이벤트 발행
                        ┌──────────▼───────────┐
                        │  internal/eventbus    │  구독형 이벤트 채널
                        └──────────┬───────────┘
                     ┌─────────────┼─────────────┐
          ┌──────────▼───────┐ ┌───▼─────────┐ ┌──▼──────────────┐
          │ internal/logging  │ │ internal/   │ │ internal/webui    │
          │ (log/slog sink)   │ │ audit       │ │ (SSE로 실시간 push) │
          └───────────────────┘ └─────────────┘ └────────────────────┘
```

### 4.2 디렉터리 구조 예시

```
dodaemon/
├── cmd/
│   └── dodaemon/          # main.go — CLI 엔트리포인트 (cobra)
├── internal/
│   ├── config/            # 설정 로드/검증/핫리로드, 스키마 정의
│   ├── ftp/                # FTP 서버 구현 (또는 서드파티 래핑)
│   ├── tftp/               # TFTP 서버 구현
│   ├── syslog/             # syslog 리시버 + RFC3164/5424 파서
│   ├── webui/               # net/http 핸들러, embed된 정적 자산
│   ├── eventbus/            # 공통 이벤트 버스 (pub/sub)
│   ├── auth/                 # 가상 사용자 인증, bcrypt
│   ├── audit/                 # 감사 로그 기록
│   ├── security/               # 경로 검증, IP ACL 공통 유틸
│   └── service/                 # Windows Service / systemd 등록 로직
├── web/
│   └── ui/                        # 웹 UI 소스 (빌드 산출물을 embed)
├── configs/
│   └── default.yaml                # 기본 설정 템플릿 (embed)
├── docs/
├── scripts/                          # 빌드/릴리스 스크립트
├── go.mod
└── PLAN.md
```

### 4.3 서버 생명주기 관리

- 각 서버(`ftp`, `tftp`, `syslog`)는 공통 인터페이스를 구현한다.

```go
type Server interface {
    Name() string
    Start(ctx context.Context) error
    Shutdown(ctx context.Context) error   // graceful: 진행 중 전송 완료 대기 후 종료
}
```

- `main()`에서 `errgroup.Group` 또는 자체 supervisor로 세 서버를 병렬 기동하고, `os/signal`(SIGINT/SIGTERM, Windows는 서비스 Stop 신호)로 최상위 `context.Context`를 취소해 전 서버에 전파.
- 개별 서버 오류는 프로세스 전체를 죽이지 않고 해당 서버만 재시작(지수 백오프)하도록 supervisor 계층에서 격리.
- Graceful shutdown 타임아웃(기본 10초)을 두어 초과 시 강제 종료.

### 4.4 설정 관리 및 핫리로드

- `internal/config`가 YAML을 구조체로 언마샬 후 검증(예: 포트 범위, 디렉터리 존재 여부, ACL CIDR 유효성).
- `fsnotify`로 설정 파일 변경을 감지 → 새 설정을 파싱·검증 → 검증 통과 시에만 각 서버에 원자적으로 교체 적용(치명적 항목, 예: 리슨 포트 변경은 해당 서버만 재기동, 나머지는 무중단 반영).
- 검증 실패 시 기존 설정을 유지하고 오류를 로그+웹 UI에 노출(설정 오류로 서비스가 죽지 않도록).

### 4.5 공통 이벤트 버스 및 로깅 계층

- 각 서버가 연결/인증/전송/에러 이벤트를 `eventbus`에 발행 → `logging`(파일/콘솔 슬로그 싱크), `audit`(별도 감사 로그 파일), `webui`(Server-Sent Events로 브라우저에 실시간 스트리밍)가 각자 구독.
- 이벤트 버스는 표준 라이브러리 채널 기반의 얇은 pub/sub로 직접 구현(외부 메시지 큐 불필요 — 단일 프로세스 내 통신이므로 과설계 지양).

### 4.6 관리 UI: 웹 UI vs TUI/CLI 비교 및 권고안

| 항목 | 임베드 웹 UI (localhost) | TUI (bubbletea 등) | 순수 CLI |
|---|---|---|---|
| 원격/헤드리스 서버 관리 | ◎ 브라우저만 있으면 어디서든 | △ SSH 세션 필요 | ○ SSH/스크립트 친화적 |
| 실시간 로그 시각화(필터, 검색, 그래프) | ◎ 강력 | ○ 텍스트 기반 한계 | ✕ |
| 초기 학습 곡선(비개발자 장비 관리자) | ◎ 익숙한 브라우저 UX | △ | ✕ |
| 구현/유지보수 비용 | 중~상 (프론트엔드 자산 관리) | 중 | 하 |
| 단일 바이너리 원칙과의 정합성 | ○ (embed로 해결) | ◎ | ◎ |
| 자동화/스크립팅 연동 | ○ (REST API 겸용 가능) | △ | ◎ |

**권고안**: **임베드 웹 UI를 기본 관리 인터페이스로 채택**하고, 모든 설정 변경이 가능한 **CLI(플래그/서브커맨드)를 1급 시민으로 병행 제공**한다(무인 설치, 서비스 등록, CI 자동화용). TUI는 3CDaemon 원조가 가진 "설치 없이 바로 뜨는 관리 창"의 감성을 계승하되, 웹 UI가 이를 브라우저로 대체하므로 **TUI는 로드맵에서 제외**한다(개발 비용 대비 효용 낮음 — 브라우저 UX가 이미 이 역할을 충분히 수행). CLI는 웹 UI와 동일한 내부 API(§7)를 호출해 두 인터페이스 간 로직 중복을 방지한다.

---

## 5. 기술 스택 및 라이브러리 검토

### 5.1 기본 원칙

표준 라이브러리로 충분한 영역(HTTP, TLS, JSON, 로깅, 파일시스템)은 서드파티를 도입하지 않는다. 서드파티는 (1) 프로토콜 구현 복잡도가 높고 검증된 구현이 존재하며 (2) 순수 Go(CGO 불필요)이고 (3) 라이선스가 허용적(MIT/Apache/BSD)인 경우에만 채택한다.

### 5.2 FTP 서버 라이브러리

| 후보 | 특징 | 라이선스 | 평가 |
|---|---|---|---|
| `fclairamb/ftpserver` | 능동/수동, TLS, 가상 파일시스템 드라이버 인터페이스 제공, 활발히 유지보수 | MIT | **채택 권장** — 드라이버 인터페이스를 우리 auth/security 계층에 연결해 커스텀 권한 모델 구현 가능 |
| 직접 구현 | 완전한 제어, 외부 의존성 0 | - | 인증/PASV 포트 협상/TLS 업그레이드 등 재구현 비용 큼. 버그 위험 대비 이점 적음 |

**결론**: `fclairamb/ftpserver`를 채택하되, 인증·권한·경로 검증은 반드시 우리 `internal/auth`, `internal/security`로 감싸 라이브러리의 기본 드라이버를 신뢰하지 않는다(§8).

### 5.3 TFTP 라이브러리

| 후보 | 특징 | 라이선스 | 평가 |
|---|---|---|---|
| `pin/tftp` (v3) | 서버/클라이언트 모두 지원, RFC 2347/2348/2349 옵션 협상 내장, 순수 Go | MIT | **채택 권장** |
| 직접 구현 | RFC 1350은 프로토콜 자체가 단순(5개 패킷 타입)해 직접 구현도 현실적 | - | 옵션 협상까지 직접 하면 공수 증가. 초기엔 라이브러리로 시작 후 필요 시 대체 |

### 5.4 Syslog 파서/서버

| 후보 | 특징 | 라이선스 | 평가 |
|---|---|---|---|
| `haimrubinstein/go-syslog` / `mcuadros/go-syslog` | RFC3164+5424 파싱, UDP/TCP 서버 포함 | MIT | 서버 구현까지 포함되어 편리하나 유지보수 활발도 확인 필요 |
| `leodido/go-syslog` (구 influxdata) | RFC5424 전용 초고성능 파서(자동 생성 파서), 업계에서 rsyslog 등에 사용 검증됨 | MIT | **파서로 채택 권장** — RFC5424는 이 라이브러리, RFC3164는 자체 경량 파서 작성 |
| 직접 구현(수신 서버) | UDP/TCP 리스너 자체는 표준 라이브러리(`net`)만으로 충분히 단순 | - | **서버(리스너) 부분은 직접 구현**, 파싱만 라이브러리 활용 — 혼합 전략 |

**결론**: 리스너(UDP/TCP/TLS)는 `net`, `crypto/tls` 표준 라이브러리로 직접 구현(포맷 무관하게 재사용 가능), RFC5424 파싱은 `leodido/go-syslog` 채택, RFC3164는 정규식 기반 경량 자체 파서(포맷이 비교적 단순하고 벤더별 변형이 많아 유연한 커스터마이징이 필요하므로).

### 5.5 설정 / CLI / 서비스 / 로깅

| 영역 | 채택 | 대안 | 비고 |
|---|---|---|---|
| CLI 파싱 | `spf13/cobra` (MIT) | 표준 `flag` | 서브커맨드(serve, install-service, config validate 등) 구조에 적합 |
| YAML 파싱 | `gopkg.in/yaml.v3` (MIT/Apache) | `pelletier/go-toml` | YAML 우선 채택, TOML은 필요시 추가 지원 |
| 설정 파일 감시 | `fsnotify/fsnotify` (BSD) | 폴링 | 크로스플랫폼 파일 변경 감지 표준 사실상 표준 |
| 로깅 | 표준 `log/slog` (Go 1.21+) | `zap`, `zerolog` | 표준 라이브러리로 충분(구조화 JSON 로그 내장), 외부 의존성 최소화 원칙에 부합 |
| Windows Service/systemd | `kardianos/service` (Zlib) | `golang.org/x/sys/windows/svc` 직접 사용 | 크로스플랫폼 서비스 등록 추상화로 개발 비용 절감 |
| bcrypt 해시 | `golang.org/x/crypto/bcrypt` (BSD) | - | 사실상 표준 |

### 5.6 웹 UI 스택

- 서버: 표준 `net/http` + `embed.FS`로 정적 자산 서빙, 실시간 로그는 SSE(`text/event-stream`, 표준 라이브러리만으로 구현 가능 — 별도 WebSocket 라이브러리 불필요).
- 프런트엔드: 별도 SPA 프레임워크(React 등) 빌드 파이프라인을 프로젝트에 강제하지 않기 위해 **htmx + 최소 vanilla JS + 서버사이드 `html/template`** 조합을 우선 검토. 이렇게 하면 Node.js 빌드 툴체인 없이도 UI를 유지보수할 수 있어 "의존성 없는 단일 바이너리"라는 프로젝트 철학과 개발 환경 자체의 단순성이 일치한다. UI 요구가 커지면 별도 프론트엔드 빌드 후 산출물만 embed하는 방식으로 전환 가능(빌드 타임에만 Node 필요, 런타임 의존성은 여전히 0).

### 5.7 라이선스 요약

채택 후보 전부 MIT/BSD/Apache-2.0/Zlib 계열의 허용적 오픈소스 라이선스로, 상용 배포 및 소스 비공개 배포에 제약이 없다. `go.mod`의 라이선스는 릴리스 시 `THIRD_PARTY_NOTICES` 파일로 취합해 바이너리와 함께 배포한다(§9).

---

## 6. 설정 파일 스키마 예시 (YAML)

```yaml
# configs/default.yaml
server:
  hostname: dodaemon
  data_dir: ./data

ftp:
  enabled: true
  listen: "0.0.0.0:21"
  passive_port_range: [50000, 50100]
  tls:
    enabled: false
    cert_file: ""
    key_file: ""
  allow_anonymous: false
  users:
    - username: admin
      password_hash: "$2a$10$..."
      home_dir: "./data/ftp/admin"
      permissions: "elradfmwMT"   # 3CDaemon류 권한 문자셋 계승

tftp:
  enabled: true
  listen: "0.0.0.0:69"
  root_dir: "./data/tftp"
  allow_write: true          # WRQ(업로드) 허용 여부
  max_blksize: 65464

syslog:
  enabled: true
  udp_listen: "0.0.0.0:514"
  tcp_listen: ""
  tls:
    enabled: false
  log_dir: "./data/syslog"
  rotate:
    max_size_mb: 100
    max_age_days: 30
    compress: true

security:
  ip_allowlist: []            # 비어있으면 전체 허용(§8.6 참고 — 기본값 정책은 아래 명시)
  ip_denylist: []

webui:
  enabled: true
  listen: "127.0.0.1:8080"    # 기본값은 로컬호스트만 바인딩
  auth:
    username: admin
    password_hash: "$2a$10$..."
```

---

## 7. API/UI 설계 개요

- 웹 UI는 내부적으로 `/api/v1/*` REST 엔드포인트를 호출하며, 동일 API를 `dodaemon` CLI도 사용(§4.6에서 언급한 로직 중복 방지).
- 주요 화면: **대시보드**(3개 서버 상태·처리량 요약), **FTP 관리**(사용자/권한 편집, 현재 세션 목록), **TFTP 로그**(최근 전송 이력), **Syslog 뷰어**(실시간 tail + 필터: 소스 IP, severity, 키워드), **설정 편집**(YAML 폼 기반 편집 + 검증 결과 표시).
- 인증: 웹 UI 자체 로그인(로컬 admin 계정, bcrypt), 세션 쿠키는 `HttpOnly`+`Secure`(HTTPS 사용 시)+`SameSite=Strict`.
- 기본적으로 웹 UI는 `127.0.0.1`에만 바인딩해 외부 노출을 방지(원격 관리가 필요하면 사용자가 명시적으로 설정 변경 — §8.6 deny-by-default 원칙과 동일 선상).

---

## 8. 보안 고려사항

### 8.1 경로 탈출(Path Traversal) 방지

- FTP/TFTP 모두 클라이언트가 요청한 경로를 반드시 `filepath.Clean` 후, 설정된 루트 디렉터리의 절대 경로와 `strings.HasPrefix` 비교(구분자 경계 포함 검증)로 루트 밖 접근을 차단하는 공통 유틸(`internal/security.SafeJoin`)을 전 서버가 공유.
- 심볼릭 링크를 통한 루트 탈출도 고려해, 필요 시 `os.Lstat`로 심링크 여부 확인 후 정책적으로 거부(기본값) 또는 명시적 허용 옵션 제공.

### 8.2 TFTP의 무인증 특성 대응

- TFTP 프로토콜 자체에는 인증이 없으므로 (1) 기본값으로 WRQ(업로드)를 비활성화하고 필요한 경우에만 명시적으로 활성화, (2) 업로드 허용 시에도 IP 화이트리스트를 강력히 권고(설정 UI에서 경고 표시), (3) 루트 디렉터리를 반드시 전용 디렉터리로 격리해 시스템 파일 접근 원천 차단, (4) 업로드된 파일에 대한 크기 제한(`tsize` 기반 사전 검증) 적용.

### 8.3 권한 최소화

- 1024 미만 포트(FTP 21, TFTP 69, Syslog 514) 바인딩에 필요한 권한만 획득 후 나머지 동작은 낮은 권한으로 수행. Windows는 서비스 계정을 최소 권한(예: `NT SERVICE\dodaemon` 전용 계정 또는 LocalService)으로 등록 권장, Linux는 `setcap cap_net_bind_service` 활용 가이드 제공(root 상시 구동 지양).
- 파일 시스템 접근은 항상 설정된 데이터 디렉터리 하위로 제한(§8.1과 연동), 프로세스 자체는 필요 이상의 파일 권한을 요구하지 않음.

### 8.4 평문 프로토콜 경고

- FTP(비-TLS), TFTP, Syslog(UDP)는 프로토콜 특성상 평문 전송이 기본이다. 웹 UI와 CLI 양쪽에서 TLS 미사용 시 명확한 경고 배지를 표시하고, 최초 실행 시(First-run wizard)에도 이 사실을 고지한다. 가능한 환경에서는 FTPS/Syslog-TLS 사용을 권장 문구로 안내.

### 8.5 Syslog 로그 인젝션 방지

- 수신한 syslog 메시지를 파일/웹 UI에 저장·표시할 때, 개행 문자 및 제어 문자를 이스케이프하여 로그 위조(log forging)나 웹 UI에서의 스크립트 삽입(저장형 XSS)을 방지. 웹 UI 렌더링 시 `html/template`의 자동 이스케이프를 사용(수동 문자열 결합으로 HTML 생성 금지).
- 구조화 로그(JSON)로 저장해 필드 단위로 파싱 가능하게 하고, 원본 raw 메시지는 별도 필드에 이스케이프된 채로 보관.

### 8.6 기본값 안전성 (Deny by Default)

- 신규 설치 시 기본 설정은 다음을 원칙으로 한다: TFTP WRQ(업로드) 비활성화, FTP 익명 접속 비활성화, 웹 UI는 로컬호스트만 바인딩, IP 화이트리스트가 비어 있으면 "전체 허용"이 아니라 **최초 실행 시 강제로 화이트리스트 설정을 유도**하는 온보딩 플로우(또는 최소한 명확한 경고와 함께 전체 허용을 명시적으로 선택하게 함).
- 모든 서버는 기본적으로 비활성화 상태로 시작하며, 사용자가 필요한 서버만 명시적으로 켜는 opt-in 구조.

---

## 9. 배포 및 운영

### 9.1 크로스 컴파일 매트릭스

| OS | Arch | 우선순위 |
|---|---|---|
| windows | amd64 | 1순위(주 타깃) |
| windows | arm64 | 3순위 |
| linux | amd64 | 2순위 |
| linux | arm64 | 3순위(라즈베리파이 등 임베디드 관리 서버) |
| darwin | amd64/arm64 | 3순위 |

`GOOS`/`GOARCH` 조합 + `CGO_ENABLED=0`로 전부 순수 Go 크로스 컴파일. `goreleaser`로 태그 push 시 전 플랫폼 빌드·아카이브·체크섬(SHA256) 생성 자동화(CI: GitHub Actions).

### 9.2 코드 서명

- **Windows**: Authenticode 코드 서명 인증서(EV 권장 — SmartScreen 신뢰도 확보에 유리) 적용, `signtool`로 CI 파이프라인에서 서명. 서명 없는 배포 시 Windows Defender SmartScreen 경고 발생 가능성 고지.
- **macOS**: Apple Developer ID로 서명 + notarization(공증) 필요(Gatekeeper 통과).
- 인증서/서명 비용 및 절차는 프로젝트 배포 단계(v1.0)에서 별도 검토 항목으로 관리(초기 릴리스는 미서명 배포 + 체크섬 검증 안내로 대체 가능).

### 9.3 자동 업데이트

- v1.0 범위에서는 **자동 업데이트 미도입**을 권고. 네트워크 장비 관리 도구 특성상 관리자가 통제된 시점에 수동으로 버전을 올리는 것을 선호하는 경우가 많고, 자동 업데이트는 공급망 공격 표면을 늘린다. 대신 GitHub Releases 기반 배포 + 웹 UI 대시보드에 "새 버전 알림"(수동 확인, 자동 다운로드 없음) 정도만 제공.

### 9.4 Windows 방화벽 / 1024 미만 포트 바인딩

- 설치/서비스 등록 커맨드(`dodaemon install-service`) 실행 시 관리자 권한을 요구하고, 그 안에서 `netsh advfirewall firewall add rule`로 사용 중인 포트(FTP 21+PASV 범위, TFTP 69, Syslog 514, 웹 UI 8080)에 대한 인바운드 규칙을 자동 등록(옵션으로 스킵 가능).
- 1024 미만 포트 바인딩은 Windows에서는 관리자 권한 프로세스/서비스로 실행 시 자연히 해결되며, Linux는 §8.3의 `setcap` 가이드를 문서화.

### 9.5 서비스 설치

- `dodaemon install-service` : Windows에서는 `kardianos/service`를 통해 Windows Service로 등록(시작 유형: 자동, 복구 옵션: 실패 시 재시작), Linux에서는 systemd unit 파일(`/etc/systemd/system/dodaemon.service`)을 생성 후 `systemctl enable --now`.
- `dodaemon uninstall-service`, `dodaemon service status` 등 대응 서브커맨드 제공.
- 서비스 모드와 포그라운드(콘솔) 모드를 동일 바이너리·동일 설정 파일로 전환 가능하게 해 개발/디버깅 시 편의성 확보.

---

## 10. 테스트 전략

- **유닛 테스트**: 프로토콜 파서(syslog RFC3164/5424, TFTP 옵션 협상), 경로 검증(`SafeJoin`), 설정 검증 로직에 집중, 테이블 기반 테스트로 RFC 엣지 케이스 커버.
- **통합 테스트**: `httptest`/실제 로컬 포트 바인딩으로 FTP/TFTP/Syslog 클라이언트-서버 왕복 테스트(Go 표준 라이브러리 클라이언트 또는 위 서드파티 라이브러리의 클라이언트 기능 활용).
- **보안 테스트**: 경로 탈출 페이로드(`../../etc/passwd` 등), 로그 인젝션 페이로드에 대한 회귀 테스트 케이스 고정.
- **크로스플랫폼 CI**: GitHub Actions matrix(windows-latest, ubuntu-latest, macos-latest)에서 빌드+테스트.
- **부하 테스트**: TFTP 동시 세션, Syslog 초당 메시지 처리량에 대한 벤치마크(`go test -bench`)를 v0.3 단계부터 도입.

---

## 11. 로드맵 및 마일스톤 (제안)

| 마일스톤 | 목표 | 산출물 |
|---|---|---|
| M1 (4주) | 코어 아키텍처 + TFTP MVP | `internal/config`, `internal/tftp`, CLI 골격 |
| M2 (4주) | FTP MVP + Syslog MVP(UDP) | `internal/ftp`, `internal/syslog`, 통합 CLI |
| M3 (3주) | 웹 UI 대시보드 + 실시간 로그 뷰어 | `internal/webui`, SSE 스트리밍 |
| M4 (3주) | TLS(FTPS, Syslog-TLS), IP ACL, 핫리로드 | 보안 기능 일체 |
| M5 (2주) | 서비스 등록(Windows/Linux), 로그 로테이션 | `internal/service` |
| M6 (2주) | 크로스 컴파일 릴리스 파이프라인, 문서화, 코드 서명 검토 | v1.0 배포 |

(기간은 1인 개발 기준 추정치이며 실제 착수 시 재조정 필요)

---

## 12. 리스크 및 오픈 이슈

- **FTP 라이브러리 종속 리스크**: `fclairamb/ftpserver`의 드라이버 인터페이스가 향후 우리의 세밀한 권한 모델(디렉터리별 권한 매트릭스)을 완전히 수용하지 못할 경우 부분적 포크 또는 커스텀 드라이버 레이어 확장이 필요할 수 있음 — M2 착수 시 PoC로 조기 검증 필요.
- **RFC3164 파싱의 비표준 변형**: 다양한 네트워크 장비 벤더가 RFC3164를 변형해 구현하는 경우가 많아, 자체 파서가 실제 장비 로그 샘플로 검증되어야 함 — 사용자 대상 베타에서 실제 장비 로그 수집·튜닝 필요.
- **웹 UI 프런트엔드 기술 확정 미결**: htmx 기반 경량 접근과 SPA 프레임워크 사이의 최종 결정은 M3 착수 직전 UI 요구사항 구체화 후 재확인 권고.
- **코드 서명 비용/일정**: EV 인증서 취득 절차(신원 확인 등)에 시간이 소요될 수 있어 v1.0 일정에 영향 가능 — M5 시점에 조기 신청 권장.
- **자동 업데이트 정책**: §9.3에서 미도입을 권고했으나, 사용자 피드백에 따라 v1.x 이후 옵트인 방식의 업데이트 확인 기능 추가 여부는 재논의 여지 있음.
