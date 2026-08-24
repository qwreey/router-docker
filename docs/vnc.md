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

이 탭은 **새로운 중계 방식이 아니라 [App Routes](app-routes.md) 위에 얹힌
얇은 층**입니다. 중요한 제약이 하나 있어서 그렇습니다:

> router의 Caddy는 stock 빌드(layer4 플러그인 없음)라 **HTTP/WebSocket만**
> 중계할 수 있습니다. VNC의 원래 프로토콜인 raw RFB(바이너리 TCP, 보통
> 5900번)는 App Routes로도 Dev Proxy로도 태울 수 없습니다.

그래서 여기서 실제로 중계되는 건 raw RFB가 아니라, 대상 컨테이너가 wayvnc
같은 VNC 서버 **앞에 따로 띄워둔 웹 VNC 프런트엔드**(noVNC + websockify,
보통 6080번)의 HTTP+WebSocket입니다. 정리하면:

```
브라우저 <─HTTP/WS─> router nginx (/app/<이름>/)
                        └─> router Caddy (handle_path)
                              └─> 대상:6080  websockify + noVNC
                                    └─raw RFB(컨테이너 내부)─> wayvnc
```

이 탭에서 대상을 하나 등록하면 **같은 이름의 App Route가 자동으로 함께
생성**되고, 삭제하면 함께 삭제됩니다. App Routes 탭에 가면 그 항목이 평범한
앱 하나로 그대로 보입니다 — 숨겨진 별도 경로가 아닙니다. 이 탭이 따로
가지고 있는 건 App Route가 알 필요 없는 두 가지뿐입니다: 사람이 읽는 표시
이름과, 어떤 뷰어 백엔드의 URL 모양을 만들지(아래 "뷰어 백엔드").

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
- **대상 (host:port)** — **웹 VNC 포트**입니다. `vnc-only:6080`처럼
  compose 서비스 호스트네임이나 네트워크 별칭을 쓰세요. `5900`번대를 넣으면
  폼이 경고를 띄웁니다(위 "어떻게 동작하는가" 참고).
- **뷰어 백엔드** — 아래 "뷰어 백엔드" 참고.
- **인증 요구 (tinyauth)** — App Routes의 같은 옵션과 완전히 동일합니다
  ([인증](app-routes.md#인증) 참고). 최소 한 명의 tinyauth 사용자가 등록되어
  있어야 실제로 로그인할 수 있습니다.

### 대상 호스트 allowlist

대상 호스트는 App Routes/Dev Proxy와 **같은 allowlist**를 통과해야 합니다
(`internal/targetguard`). 기본 허용 목록은 `code-docker`와 `dind`뿐이므로,
sibling 프로젝트의 컨테이너를 대상으로 삼으려면 `.env.router`의
`ROUTER_EXTRA_ALLOWED_TARGET_HOSTS`에 그 호스트를 추가해야 합니다:

```sh
ROUTER_EXTRA_ALLOWED_TARGET_HOSTS=vnc-only
```

router 자신을 가리키는 주소(`localhost`, `router`, `forward` 등)는 이
설정과 무관하게 **언제나** 거부됩니다.

## 뷰어 백엔드

지금 구현되어 있는 백엔드는 **noVNC** 하나뿐입니다. 그런데도 선택 항목으로
되어 있는 건, 이 기능이 결국 router에 붙는 임의의 GUI 컨테이너를 겨냥한
범용 기능이고 **대상마다 필요한 스택이 다를 수 있기 때문**입니다 —
redis-insight류의 단순 GUI 툴은 noVNC로 충분하지만, 카메라 회전·드래그가
잦은 3D 인터랙션(Roblox Studio 등)은 결국 지연시간이 더 낮은 스택이
필요해질 가능성이 높습니다. 두 번째 백엔드(Selkies)를 추가하는 작업이
스키마 변경 없이 항목 하나 추가로 끝나도록 처음부터 이 모양으로 만들어
두었습니다.

백엔드 목록은 router-manager가 서버에서 내려주므로, 백엔드가 추가되면
프런트엔드 수정 없이 선택지에 나타납니다.

## 보기 (뷰어)

목록에서 "보기"를 누르면 같은 페이지 아래에 뷰어가 열립니다.

- **전체화면** — iframe 자체에 fullscreen을 요청합니다. noVNC 자신의
  전체화면 버튼은 가장 안쪽 문서만 볼 수 있어서, webmanager에 임베드된
  상태(webmanager → router → 뷰어, 중첩 iframe 2단)에서는 자기 프레임
  크기까지만 커집니다 — 그래서 바깥에서 요청하는 이 버튼을 따로 두었습니다.
- **새 탭** — 뷰어를 독립된 탭으로 엽니다. 전체화면이 정책상 거부되는
  환경에서의 대안이기도 합니다.

### 전용 관리 도메인(`ROUTER_MANAGER_HOSTS`)에서는 뷰어가 열리지 않습니다

`ROUTER_MANAGER_HOSTS`로 만든 전용 도메인은 **router-manager 자신만**
서비스하고 `/app/`은 서비스하지 않습니다 — 사용자가 등록한(즉 신뢰할 수 없는)
앱 콘텐츠를 router-manager와 같은 origin에 두지 않기 위한 의도적인 설계이고,
그 origin 격리가 이 기능의 존재 이유이기도 합니다([보안: 공유 origin과 전용
도메인](router.md#보안-공유-origin과-전용-도메인routermanagerhosts) 참고).

그래서 전용 도메인을 **직접** 열어 VNC 탭에 들어가면 뷰어 대신 안내 문구가
나옵니다. 실제 사용 경로 두 가지는 정상 동작합니다:

- **webmanager의 VNC 탭** — webmanager가 자신의 origin(공유 호스트네임)을
  임베드에 알려주므로, 전용 도메인을 쓰는 구성에서도 뷰어가 정상적으로
  열립니다.
- **공유 호스트네임의 `/router/`** — `/app/`이 같은 origin에 있으므로 그대로
  동작합니다.

## App Route가 어긋났을 때

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
  웹 경로에서는 `VNC_PASSWORD` 대신 위 **인증 요구(tinyauth)** 로
  게이팅하세요 — 네이티브 클라이언트로 raw 포트에 붙는 경로에서는
  `VNC_PASSWORD`가 그대로 잘 동작합니다.
- **소프트웨어 인코딩입니다.** wayvnc의 하드웨어 H.264 인코딩(`--gpu`,
  DMA-BUF + VAAPI)은 이 탭과 무관하게 대상 컨테이너 쪽에서 따로 켜야 하고,
  GPU 벤더에 따라 동작 이력이 갈립니다(NVIDIA는 상류 이슈가 열려 있음).
