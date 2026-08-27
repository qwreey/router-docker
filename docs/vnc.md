# VNC

router에 붙어 있는 GUI 컨테이너(예: Wine/labwc 위에서 Roblox Studio를 띄우는
[roblox-studio-docker](https://github.com/qwreey/roblox-studio-docker))의 화면을
**브라우저 대시보드 안에서 바로 보고 조작**하기 위한 탭입니다. 네이티브 VNC
클라이언트(TigerVNC/KRDC 등)를 따로 띄울 필요가 없습니다.

router가 이 기능을 갖는 게 자연스러운 이유는 이미 code-docker의 네트워크
경계를 전담하고 있기 때문입니다 — GUI 컨테이너를 바깥에 전혀 노출하지 않고
`internal: true` 네트워크에만 붙여둔 채, 그 네트워크에 닿을 수 있는 유일한
컨테이너인 router만 화면을 중계하게 만들 수 있습니다.

## 어떻게 동작하는가 (읽고 시작하는 편이 좋습니다)

이 탭은 대상마다 두 가지 **뷰어 백엔드** 중 하나를 고릅니다 — 어느 쪽을
고르느냐에 따라 실제로 중계되는 프로토콜과 경로가 완전히 달라집니다.

> router의 Caddy는 stock 빌드(layer4 플러그인 없음)라 **HTTP/WebSocket만**
> 중계할 수 있습니다. VNC의 원래 프로토콜인 raw RFB(바이너리 TCP, 보통
> 5900번)는 App Routes로도 Dev Proxy로도 태울 수 없습니다 — 이 제약 자체는
> 지금도 그대로입니다.

**`rfb` (router 중계) — 기본값.** router-manager가 이 제약을 자기 자신의
코드로 우회합니다: noVNC 뷰어를 router-manager 프로세스 안에서 직접
서비스하고, 브라우저의 WebSocket을 대상의 **raw RFB 포트**(네이티브 클라이언트가
붙는 것과 같은 포트, 예: `vnc-only:5900`)에 직접 브리지합니다.

```
브라우저 <─HTTP/WS─> router-manager (noVNC 뷰어 + RFB 브리지)
                        └─raw RFB─> 대상:5900 (wayvnc 등)
```

대상은 VNC만 말하면 되고 웹 서버도 noVNC도 websockify도 필요 없습니다. App
Route를 아예 만들지 않으므로 App Routes 탭에는 아무것도 나타나지 않고,
`/app/<이름>/` 경로도 존재하지 않으며, 아래 "App Route가 어긋났을 때"에서
설명하는 드리프트 경고도 애초에 있을 수 없습니다.

**`novnc` (대상이 서비스하는 웹 VNC) — 이전까지의 유일한 방식.** 대상
컨테이너가 wayvnc 같은 VNC 서버 **앞에 따로 띄워둔 웹 VNC 프런트엔드**
(noVNC + websockify, 보통 6080번)의 HTTP+WebSocket을 router가 App Route로
그대로 리버스 프록시합니다.

```
브라우저 <─HTTP/WS─> router nginx (/app/<이름>/)
                        └─> router Caddy (handle_path)
                              └─> 대상:6080  websockify + noVNC
                                    └─raw RFB(컨테이너 내부)─> wayvnc
```

`novnc` 백엔드로 대상을 하나 등록하면 **같은 이름의 App Route가 자동으로
함께 생성**되고, 삭제하면 함께 삭제됩니다. App Routes 탭에 가면 그 항목이
평범한 앱 하나로 그대로 보입니다 — 숨겨진 별도 경로가 아닙니다. `rfb`
백엔드는 App Route를 만들지 않으므로 이 관계 자체가 없습니다.

두 백엔드가 진짜로 다른 전송 방식을 쓰는 이유는, 모든 프런트엔드를 노VNC로
옮겨올 수 있는 게 아니기 때문입니다 — 예를 들어 Selkies는 대상 자신의
컴포지터를 직접 캡처하는 방식이라 브리지할 raw RFB 포트 자체가 없고, 그
웹 프런트엔드를 프록시하는 것 말고는 router 쪽에서 할 수 있는 게 없습니다.
`novnc` 백엔드가 폐기되지 않고 남아 있는 이유입니다.

대상을 편집해서 백엔드를 바꾸면 App Route도 그에 맞춰 생성/갱신/삭제됩니다 —
`novnc`에서 `rfb`로 바꾸면 기존 `/app/<이름>/` 경로가 계속 서비스되는 게
아니라 지워집니다.

### 네이티브 클라이언트로 붙고 싶다면

이 탭이 아니라 **Net 관리 탭의 Forwards**를 쓰세요 — netgate의 raw TCP
포트포워딩이라 5900번 같은 raw RFB 포트를 그대로 노출할 수 있습니다. 두
경로는 서로 배타적이지 않아서, 같은 대상에 대해 웹 뷰어와 네이티브 클라이언트를
나란히 열어둘 수 있습니다.

## 대상 추가하기

"대상 추가"를 누르면 다음 값을 입력합니다:

- **이름 (표시용)** — 목록과 뷰어 제목에만 쓰이는 자유 문자열입니다. 비워두면
  아래 경로 세그먼트가 대신 표시됩니다.
- **경로 세그먼트** — 자동 생성되는 App Route의 이름이자 뷰어가 열리는
  경로(`/app/<이름>/`)입니다. App Routes와 같은 제약(소문자/숫자/하이픈, 점
  불가)을 따릅니다. 이미 같은 이름의 App Route가 있으면 거부됩니다 — 남의
  앱을 가로채지 않기 위한 의도된 동작입니다.
- **대상 (host:port)** — 어떤 포트를 넣어야 하는지가 백엔드에 따라
  **정반대**입니다. `rfb`는 raw RFB 포트(보통 `5900`, 예: `vnc-only:5900`)를,
  `novnc`는 웹 VNC 포트(보통 `6080`, 예: `vnc-only:6080`)를 원합니다. 어느
  쪽이든 compose 서비스 호스트네임이나 네트워크 별칭을 쓰세요. 선택한
  백엔드에 안 맞아 보이는 포트를 넣으면 폼이 경고를 띄웁니다.
- **뷰어 백엔드** — `rfb`(RFB, router 중계) 또는 `novnc`(대상이 서비스하는
  웹 VNC). 대상이 어떤 스택인지에 따라 정해지는 값이지 취향 문제가
  아닙니다. 자세한 차이는 아래 "뷰어 백엔드"와 위 "어떻게 동작하는가" 참고.
- **창 크기 변경 처리** — 아래 "창 크기 변경 처리" 참고.
- **인증 요구** — 백엔드에 따라 메커니즘 자체가 다릅니다.
  - `novnc` 백엔드에서는 App Routes의 같은 옵션과 완전히 동일한 tinyauth
    게이팅입니다([인증](app-routes.md#인증) 참고). 최소 한 명의 tinyauth
    사용자가 등록되어 있어야 실제로 로그인할 수 있고, tinyauth 로그인 화면을
    서비스할 `TINYAUTH_HOSTS`도 설정되어 있어야 합니다 — 비어 있으면 "인증
    요구"를 켠 대상은 전부 접속 불가가 됩니다. 자세한 내용은
    [router.md#tinyauth](router.md#tinyauth) 참고.
  - `rfb` 백엔드에는 이 체크박스 자체가 표시되지 않습니다. App Route가 없으니
    tinyauth가 걸릴 자리도 없고, 접근 제어는 router-manager 자신의
    비밀번호(설정 탭)가 대신합니다. router-manager가 잠겨 있지 않다면 그
    URL을 아는 누구나 뷰어를 열 수 있다는 뜻이므로, API는 `rfb` 대상에 대한
    `requireAuth: true`를 조용히 무시하는 대신 **거부**합니다 — 로그인이
    걸려 있다고 믿게 만들고 실제로는 활짝 열어두는 상황을 막기 위해서입니다.
    목록의 인증 열에는 "router 잠금"이라고 표시되고, router-manager 자체가
    잠겨 있으면 뷰어 대신 그 사실을 안내하는 문구가 뜹니다(연결이 조용히
    실패하는 뷰어를 보여주지 않습니다).

### 대상 호스트 allowlist

대상 호스트는 백엔드와 무관하게 두 경우 모두 App Routes/Dev Proxy와 **같은
allowlist**를 통과해야 합니다(`internal/targetguard`). 기본 허용 목록은
`code-docker`와 `dind`뿐이므로,
sibling 프로젝트의 컨테이너를 대상으로 삼으려면 `.env.router`의
`ROUTER_EXTRA_ALLOWED_TARGET_HOSTS`에 그 호스트를 추가해야 합니다:

```sh
ROUTER_EXTRA_ALLOWED_TARGET_HOSTS=vnc-only
```

router 자신을 가리키는 주소(`localhost`, `router`, `forward` 등)는 이
설정과 무관하게 **언제나** 거부됩니다.

## 뷰어 백엔드

- **`rfb` (router 중계)** — 기본값. 위 "어떻게 동작하는가"에서 설명한 대로
  router-manager가 noVNC 뷰어와 RFB 브리지를 자체적으로 서비스합니다. 대상은
  raw RFB만 말하면 되고, 전용 관리 도메인에서도 별도 설정 없이 그대로
  동작합니다(아래 "보기" 참고).
- **`novnc` (대상이 서비스하는 웹 VNC)** — 대상이 이미 자기 노VNC 웹
  프런트엔드를 띄워둔 경우, 또는 raw RFB로 브리지할 수 없는 프런트엔드
  (Selkies처럼 대상 자신의 컴포지터를 캡처하는 방식)를 쓰는 경우를 위한
  경로입니다. App Route로 중계하므로 App Routes 탭에 그대로 보이고, tinyauth
  "인증 요구"와 `ROUTER_APP_ORIGIN` 전용 도메인 설정이 그대로 적용됩니다.

`rfb` 백엔드가 쓰는 noVNC는 router 이미지에 vendor되어 있습니다
(`router/Dockerfile`의 `NOVNC_VERSION`으로 버전 고정), roblox-studio-docker가
자기 사본에 따로 넣어야 했던 "0x0 desktop 요청 금지" 패치(아래 "창 크기 변경
처리"의 경고 참고)도 이미 포함되어 있어서, `rfb`로 붙는 대상은 전부 이 한
벌로 커버됩니다. `novnc` 백엔드는 대상이 직접 띄운 자기 자신의 noVNC 사본을
그대로 쓰므로, 그 사본에 같은 가드가 있는지는 여전히 대상 쪽 책임입니다.

백엔드 목록은 router-manager가 서버에서 내려주므로, 새 백엔드가 추가되면
프런트엔드 수정 없이 선택지에 나타납니다.

## 창 크기 변경 처리

브라우저 창(정확히는 뷰어 iframe)의 크기가 대상 화면과 다를 때 어떻게 할지를
대상마다 고를 수 있습니다.

- **원격 해상도 변경** (기본값) — 대상 서버에게 "화면을 이만큼으로 바꿔달라"고
  요청합니다(RFB `SetDesktopSize` 확장). 네이티브 클라이언트에서 창을 늘렸을
  때와 같은 동작이고, 글자가 뭉개지지 않습니다. `wayvnc`는 이 기능이 **기본으로
  켜져 있고**(`-R`/`--disable-resizing`이 끄는 쪽입니다) 헤드리스 출력을 실시간으로
  바꿔줍니다 — 실측으로 확인했습니다(뷰포트 960x634로 접속하니 대상의
  `HEADLESS-1`이 1920x1080에서 그 크기로 따라 바뀌었고, 이후 창 크기 변경도
  추적했습니다).
- **화면에 맞춰 축소** — 대상 해상도는 그대로 두고 받은 화면을 뷰어 크기에 맞춰
  축소합니다. 이 필드가 생기기 전까지의 고정 동작이었습니다.
- **아무것도 안 함** — 둘 다 하지 않습니다. 뷰어보다 크면 스크롤바가 생깁니다.

기본값이 "원격 해상도 변경"이지만 뷰어 전체에 강제하지 않고 대상마다 두는
이유는, 모든 VNC 서버가 이 요청을 받아주지는 않기 때문입니다. 고정 크기 Xvfb
앞에 붙은 x11vnc처럼 `SetDesktopSize`를 지원하지 않는 서버는 요청을 거부하고,
그러면 noVNC는 축소도 하지 않아서 **스크롤바만 생깁니다** — 그런 대상은
"화면에 맞춰 축소"를 쓰세요.

> ⚠️ **대상 쪽이 0x0 요청을 견디는지 확인하세요.** noVNC는 뷰어가 0x0 크기로
> 레이아웃된 상태(`display:none` iframe, 레이아웃을 한 번도 못 받은 페이지)에서
> **0x0 해상도를 그대로 요청합니다** — 자체 하한선이 없습니다. wayvnc는 이걸
> 그대로 wlr-output-management custom mode로 넘기고, wlroots는 폭/높이가 0
> 이하인 모드를 *프로토콜 에러*로 거부하며, libwayland는 프로토콜 에러를 치명적
> 오류로 처리합니다 — 즉 **wayvnc 프로세스가 죽습니다**. 대상 컨테이너가
> "VNC가 죽으면 컨테이너도 죽는다"식 감시를 하고 클라이언트가 자동
> 재접속까지 한다면 그대로 재시작 루프가 됩니다(roblox-studio-docker에서 실제로
> 발생, 2026-08-25 — 그쪽은 vendored noVNC에 1x1 하한 가드를 넣어 해결했고
> 자세한 내용은 그 저장소의 `CLAUDE.md` 참고). `rfb` 백엔드가 쓰는, router가
> 자체 vendor한 noVNC에는 이 가드가 이미 들어 있습니다 — 대상 자신이 자기
> noVNC 사본을 띄우는 `novnc` 백엔드에서, 그 사본에 같은 가드가 없다면 이
> 옵션 대신 "화면에 맞춰 축소"를 고르세요.

한 가지 더: noVNC는 크기를 **디바이스 픽셀** 단위로 요청합니다. 그래서 HiDPI
화면에서는 그만큼 더 큰 해상도를 요구하게 되고(창 너비 1400px, DPR 1.2에서
1680px을 요청하는 것을 실측), 대상이 렌더링·인코딩해야 할 양도 그만큼
늘어납니다. 무거운 대상이라면 이 점을 감안하세요.

`novnc` 백엔드에서는 이 값을 바꿔도 App Route는 바뀌지 않습니다 — 뷰어 URL의
쿼리만 달라지므로, "App Route가 다른 곳을 가리킵니다" 경고와는 무관합니다.
`rfb` 백엔드는 애초에 App Route가 없으므로 이 항목도 마찬가지로 무관합니다.
이미 등록해 둔(이 필드가 생기기 전의) 대상은 저장된 값이 비어 있는데, 그건
"기본값"을 뜻하므로 자동으로 "원격 해상도 변경"으로 동작합니다.

## 보기 (뷰어)

목록에서 "보기"를 누르면 같은 페이지 아래에 뷰어가 열립니다.

- **전체화면** — 가능하면 **noVNC 자신의 전체화면 버튼을 대신 눌러줍니다.**
  바깥에서 iframe에 직접 fullscreen을 걸면 화면은 커지지만 iframe *안쪽*
  문서에는 `fullscreenElement`가 설정되지 않아서, noVNC는 자기가 전체화면인 줄
  모릅니다 — 그 상태로 noVNC의 전체화면 버튼을 누르면 나가는 게 아니라 전체화면을
  한 번 더 들어가서, 빠져나오려면 "＞"를 열고 그 버튼을 **두 번** 눌러야 했습니다.
  주인을 noVNC 하나로 통일해서 어느 쪽 버튼을 눌러도 한 번에 토글되게 했고, 이
  버튼의 라벨도 현재 상태를 따라갑니다(전체화면 ↔ 전체화면 해제).
  noVNC 쪽 버튼에 손이 닿지 않는 경우(전용 도메인을 쓰는 cross-origin 임베드
  등)에는 iframe이 아니라 **뷰어 카드 전체**를 전체화면으로 만듭니다 — 그러면
  헤더가 화면에 남아서 이 버튼으로 그대로 빠져나올 수 있습니다.
- **새 탭** — 뷰어를 독립된 탭으로 엽니다. 전체화면이 정책상 거부되는
  환경에서의 대안이기도 합니다.

### `rfb` 백엔드는 전용 관리 도메인에서도 그냥 열립니다

`rfb` 백엔드는 App Route도 `/app/`도 거치지 않고 router-manager 자신이 뷰어와
RFB 브리지를 직접 서비스합니다. 그래서 `ROUTER_MANAGER_HOSTS`로 만든 전용
도메인을 **직접** 열어도 추가 설정 없이 그대로 동작하고, `ROUTER_APP_ORIGIN`은
이 백엔드와 아무 관계가 없습니다.

### `novnc` 백엔드와 전용 관리 도메인(`ROUTER_MANAGER_HOSTS`)

아래 내용은 `novnc` 백엔드로 등록한 대상에만 해당합니다.

`ROUTER_MANAGER_HOSTS`로 만든 전용 도메인은 **router-manager 자신만**
서비스하고 `/app/`은 서비스하지 않습니다 — 사용자가 등록한(즉 신뢰할 수 없는)
앱 콘텐츠를 router-manager와 같은 origin에 두지 않기 위한 의도적인 설계이고,
그 origin 격리가 이 기능의 존재 이유이기도 합니다([보안: 공유 origin과 전용
도메인](router.md#보안-공유-origin과-전용-도메인routermanagerhosts) 참고).

그래서 전용 도메인을 **직접** 열어 VNC 탭에서 `novnc` 백엔드 대상을 보려고
하면 뷰어 대신 안내 문구가 나옵니다. 실제 사용 경로 두 가지는 정상 동작합니다:

- **webmanager의 VNC 탭** — webmanager가 자신의 origin(공유 호스트네임)을
  임베드에 알려주므로, 전용 도메인을 쓰는 구성에서도 뷰어가 정상적으로
  열립니다.
- **공유 호스트네임의 `/router/`** — `/app/`이 같은 origin에 있으므로 그대로
  동작합니다.

전용 도메인을 **직접** 열었을 때도 `novnc` 백엔드 뷰어를 띄우고 싶다면
`ROUTER_APP_ORIGIN`(`example-env.router`)에 `/app/`을 실제로 서비스하는 공유
호스트네임을 `https://code.example.com`처럼 적어두세요 — SPA가 뷰어 iframe을
그 origin에서 불러옵니다(iframe만 cross-origin이 되며, 그 분리 자체가 전용
도메인이 존재하는 이유이므로 문제가 아닙니다). `rfb` 백엔드만 쓴다면
`ROUTER_APP_ORIGIN`을 설정할 필요가 전혀 없습니다 — 자세한 내용은
[router.md의 관련 절](router.md#보안-공유-origin과-전용-도메인routermanagerhosts)
참고.

## App Route가 어긋났을 때

아래 내용은 `novnc` 백엔드로 등록한 대상에만 해당합니다 — `rfb` 백엔드는 App
Route를 만들지 않으므로 이 드리프트 자체가 있을 수 없습니다.

목록에 다음 경고가 뜰 수 있습니다:

- **App Route 없음** — App Routes 탭에서 그 앱을 지웠을 때입니다. 해당
  대상을 "편집" 후 저장하면 다시 만들어집니다(삭제 후 재등록할 필요 없음).
- **App Route가 다른 곳을 가리킵니다** — App Routes 탭에서 target이나 인증
  설정을 바꿨거나, 원본 편집으로 손댔을 때입니다. 어느 쪽이 맞는지는 사람이
  판단해야 하므로 자동으로 되돌리지 않습니다 — App Routes 탭에서 확인하세요.

이 탭은 자기가 뭘 썼는지 캐시하지 않고 매번 App Routes를 다시 읽어 비교하기
때문에, 반대쪽에서 손댄 변경이 조용히 묻히지 않고 이렇게 드러납니다.

## 알려진 제약

- **noVNC와 VNC 비밀번호(VeNCrypt)를 함께 쓸 수 없습니다.** wayvnc에
  `VNC_PASSWORD`를 설정하면 wayvnc가 VeNCrypt X509Plain 보안 타입을
  요구하는데, 현재 noVNC 릴리스가 이 서브타입을 구현하지 않아
  `Unsupported security types (types: 262)`로 연결이 실패합니다. 배선
  문제가 아니라 실제 버전 호환성 문제입니다(raw RFB 프로브로 원인 확인함).
  이 한계는 백엔드와 무관하게 noVNC 자체의 한계라 `rfb`/`novnc` 모두에
  적용됩니다. 웹 경로에서는 `VNC_PASSWORD`로 게이팅하는 대신, `novnc`
  백엔드는 위 **인증 요구(tinyauth)**를, `rfb` 백엔드는 router-manager 자신의
  비밀번호를 쓰세요 — 네이티브 클라이언트로 raw 포트에 붙는 경로에서는
  `VNC_PASSWORD`가 그대로 잘 동작합니다.
- **사실상 소프트웨어 인코딩입니다.** wayvnc의 하드웨어 H.264
  인코딩(`--gpu`, DMA-BUF + VAAPI)은 이 탭과 무관하게 대상 컨테이너 쪽에서 따로
  켜야 하고(roblox-studio-docker라면 `VNC_GPU`), 켜더라도 **클라이언트가 H.264를
  협상해야만** 실제로 쓰입니다. noVNC 1.6은 WebCodecs로 H.264를 지원하지만 조건이
  둘입니다: (1) secure context — 평문 HTTP로 접속하면 `VideoDecoder` 자체가 없어
  H.264를 제안조차 하지 않습니다(router 뒤에 Caddy로 HTTPS를 물리는 통상적인
  구성이면 통과합니다), (2) 브라우저가 noVNC의 H.264 probe 프레임을 실제로
  디코드할 것. 실측(2026-08-25, Chrome 151 + AMD VAAPI)에서는 secure context에서도
  이 probe가 하드웨어 디코더에서 실패해(`prefer-software`로는 성공) noVNC가 H.264를
  비활성화했고, wayvnc는 `--gpu`를 켠 채로도 `tight` 인코딩을 골랐습니다. 즉 GPU
  인코딩이 켜져 있어도 실제로 쓰이는지는 브라우저·GPU 조합에 달려 있습니다.
