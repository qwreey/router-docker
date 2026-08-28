# vhost — 사이드 프로젝트에 호스트네임 통째로 주기

`ROUTER_VHOST_<이름>="<host>[,<host>...]=<upstream>[:port]"` 를 router 컨테이너의 환경변수로
주면, router의 nginx가 그 호스트네임을 위한 `server{}` 블록을 하나 만들어 그대로
프록시합니다. `ROUTER_MANAGER_HOSTS`/`TINYAUTH_HOSTS`가 router-manager와 tinyauth에게
전용 도메인을 주는 것과 **완전히 같은 모양**입니다.

```
바깥 리버스 프록시           router (nginx)                 사이드 프로젝트
note.example.com  ─────────> server_name note.example.com ─> trilium:8080
                  routerip:80   (path rewrite 없음)
```

## 언제 쓰나 — App Routes / Dev Proxy와의 구분

| | 주소 | 인증 | 언제 |
|---|---|---|---|
| **App Routes** | `<공유호스트>/app/<이름>/` | 앱 단위 on/off | 경로 프리픽스 밑에서 잘 도는 앱을 빠르게 붙일 때 |
| **Dev Proxy** | `<자기호스트>/...` (단, 바깥에서 `/exports` rewrite 필요) | **경로별** on/off | dev 서버, 경로마다 공개/비공개를 나눠야 할 때 |
| **vhost** | `<자기호스트>/` | 없음 (바깥 프록시의 몫) | 앱이 자기 origin을 가져야 할 때 |

"자기 origin을 가져야 할 때"가 구체적으로 무슨 뜻이냐면:

- **앱이 서브패스에서 못 돕니다.** 루트 절대경로로 에셋/API를 참조하거나 base-path 설정이
  없는 앱이 여기 해당합니다(tinyauth 자신이 정확히 그래서 전용 도메인을 씁니다).
- **origin을 공유하면 안 됩니다.** 공유 호스트네임에는 code-server와 webmanager가 같이
  있고, webmanager에는 터미널(루트 셸)과 파일 API가 있습니다. 그 앱 안에서 도는 스크립트가
  same-origin으로 그 API에 닿는 게 곤란하다면(예: 사용자·에이전트가 작성한 콘텐츠를 그대로
  실행하는 노트 앱) origin을 나눠야 합니다.

반대로 **tinyauth 경로별 "인증 요구"가 필요하면 vhost가 아니라 Dev Proxy**를 쓰세요.
vhost는 통과만 시키고 인증은 바깥 리버스 프록시(Authentik forward-auth 등)의 몫입니다.

## 설정

### 1) 값 넣기

보통은 **사이드 프로젝트 자신의 compose 오버레이**가 선언합니다 — 붙는 쪽이 자기 요구를
스스로 기술하게 하는, `netinit.*` 라벨과 같은 방향입니다:

```yaml
services:
  code-docker-router:
    environment:
      ROUTER_VHOST_TRILIUM: "${TRILIUM_HOST}=trilium:8080"
```

키 이름의 `ROUTER_VHOST_` 뒷부분은 자유입니다. **항목마다 키를 따로 두는 이유**는 두
프로젝트를 동시에 붙였을 때 하나의 콤마 목록을 서로 덮어쓰지 않게 하기 위해서입니다.

직접 관리하고 싶으면 `.env.router`에 그냥 적어도 됩니다(같은 환경변수로 들어갑니다).

### 2) 바깥 리버스 프록시

`ROUTER_MANAGER_HOSTS`/`TINYAUTH_HOSTS`와 **똑같습니다.** rewrite 없이 그대로 넘기세요:

```caddyfile
note.example.com {
    reverse_proxy http://routerip:80
}
```

인증을 걸 거라면 여기서 겁니다. PWA로 설치할 앱이라면 매니페스트와 아이콘 경로만
인증에서 빼야 합니다 — 자세한 이유는 code-docker 레포의 `docs/security-login.md`의
"PWA 설치가 안 되는 이유" 절.

### 3) upstream이 router 밖에서 닿는지

`<upstream>`은 router가 DNS로 찾을 수 있어야 합니다 — 보통 `code-docker-internal`에 붙은
컨테이너의 서비스명/별칭입니다. router 자신을 가리키는 값
(`localhost`/`127.0.0.1`/`::1`/`router`/`forward`)은 거부합니다.

## 동작 세부

- **대상 컨테이너가 안 떠 있어도 router는 정상 기동합니다.** upstream을 nginx 변수로
  넘기고 `resolver`로 요청 시점에 해석하기 때문입니다. 대상이 없으면 그 호스트네임만
  502가 나고, router 전체는 멀쩡합니다. (`proxy_pass`에 이름을 직접 쓰면 nginx가 기동
  시점에 해석하려다 실패해서 router가 통째로 안 뜹니다 — 그래서 이렇게 합니다.)
- WebSocket 업그레이드, `client_max_body_size 0`, `proxy_read_timeout 3600s`가 기본으로
  들어갑니다.
- `X-Forwarded-For`/`X-Forwarded-Proto`/`X-Real-IP`/`Host`를 전달합니다. 대상 앱이
  "신뢰하는 프록시"를 따로 설정해야 한다면(예: Trilium의 `trustedReverseProxy`)
  `code-docker-internal`의 CIDR을 넣으세요.
- router-manager의 관리 쿠키(`router_manager_unlock`)는 전달 전에 제거됩니다.
- 값이 비어 있으면 그 항목은 꺼지고, **왜 건너뛰었는지 로그에 남깁니다.** 형식이 틀렸거나
  upstream이 거부 대상이면 그 항목만 건너뛰고 나머지는 정상 생성됩니다.

## 확인

```sh
docker compose logs code-docker-router | grep 'nginx-service: vhost'
# nginx-service: vhost note.example.com -> trilium:8080 (from ROUTER_VHOST_TRILIUM)
```
