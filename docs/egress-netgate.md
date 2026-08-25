# 아웃바운드 네트워크 격리 (netgate)

code-docker 안에서 실행되는 AI 코딩 에이전트(Claude Code 등)가 프롬프트 인젝션이나
버그로 인해 컨테이너 바깥(인터넷, 특히 같은 네트워크 위 공유기/NAS 같은 사설망 장비)에
임의로 접근하지 못하게 막는 기능입니다. 전체 설계와 검토했다가 기각한 대안들은
[`.claude/archive/egress-netgate-plan-done.md`](../.claude/archive/egress-netgate-plan-done.md)에
정리되어 있습니다. 이 기능은 지금 **router** 컨테이너 안 한 기능 영역으로 통합되어
있습니다(`code-docker-router` 서비스, 예전 이름은 `code-docker-netgate`) — router의
다른 역할(tailscale, Dev Proxy 등)은 [router.md](router.md)를 확인하세요. 이 문서
안에서 "netgate"는 그 기능 영역 자체(iptables 필터링+DNS 블록리스트+DNAT)를 가리키는
이름으로 계속 씁니다.

**현재 상태: 1단계(라우팅 강제)와 2단계(`netgate`의 실제 필터링)가 모두 구현되어
있습니다.** `docker compose up`만으로 code-docker/dind의 아웃바운드가 실제로
필터링되고, 인바운드 포트 80도 정상적으로 동작합니다.

## 쉬운 설명

- code-docker/dind는 `code-docker-external`(인터넷으로 나가는 네트워크)에 직접 붙어있지
  않습니다. 대신 `code-docker-netinit-docker`라는 **호스트 쪽** 에이전트가 (2026-08-25
  이전에는 code-docker 자신의 네트워크 네임스페이스 안에서 도는 `code-docker-netinit`
  사이드카가 이 일을 했습니다 - 아래 "moby/moby#50326" 절 참고) code-docker/dind 각각의
  네트워크 네임스페이스에 밖에서 잠깐 들어가(`nsenter`) 계속 "기본 게이트웨이는
  `router`다"라는 라우트를 심어둡니다. code-docker 자신은 이 설정을 바꿀
  권한(`NET_ADMIN`)이 전혀 없으므로 - 사이드카 체제였을 때도, 지금도 마찬가지로
  - 프롬프트 인젝션으로 오염된 에이전트가 셸 명령을 마음대로 실행해도 이 라우트를
  스스로 바꿀 수 없습니다.
- `code-docker-router` 컨테이너가 실제 국경(border) 역할을 합니다 - `code-docker-internal`
  과 `code-docker-external` 양쪽에 다리를 걸치고, 사설 대역(RFC1918)으로 나가는 트래픽을
  차단하고, DNS 레벨(dnsmasq)로 도메인 블록리스트를 적용하고, 호스트의 포트 80을 자기
  자신의 nginx로 직접 받아 `code-docker`로 리버스 프록시합니다. `code-docker-internal`이 `internal: true`라
  code-docker/dind 자체의 내장 DNS는 외부로 쿼리를 포워딩하지 못하므로, router가 이들의
  DNS 리졸버 역할도 겸합니다(dnsmasq).
- **차단은 목적지 IP 기준입니다.** 같은 네트워크(`code-docker-internal`)에 붙어있는 다른
  컨테이너(dind, router 자신 등)로 가는 트래픽은 애초에 netgate 필터링을 거치지 않고
  바로 갑니다 - "그냥 아무 IP나 다 막아준다"는 뜻이 아닙니다.

### "같은 서브넷은 게이트웨이를 거치지 않는다"는 게 무슨 뜻인가요

일반적인 라우팅에서, 목적지가 **나와 같은 네트워크 대역(서브넷) 안**에 있으면 컴퓨터는
게이트웨이(라우터)에게 물어보지 않고 그 목적지에 바로 패킷을 보냅니다 - 마치 같은 건물
안 옆방에 갈 때 건물 정문 경비원을 거치지 않는 것과 같습니다. `code-docker`,
`code-docker-dind`, `code-docker-router`는 모두 `code-docker-internal`이라는 같은
서브넷 위에 있으므로, 이들끼리 주고받는 트래픽(`code-docker → dind`,
`code-docker → router` 등)은 애초에 netgate의 필터링 로직을 거치지 않습니다 - 라우팅의
기본 동작(connected route)이 게이트웨이를 자동으로 우회시키기 때문입니다. netgate의
RFC1918 차단 규칙이 `code-docker-internal` 자신의 대역(예: `172.22.0.0/16`)까지
막아버리는 게 아닌가 걱정할 필요는 없습니다 - 애초에 그 트래픽은 규칙이 적용되는 지점
(netgate의 FORWARD 체인)까지 도달하지 않기 때문입니다. 반대로 진짜 사설망(가정용
공유기 뒤 `192.168.x.x` 같은, code-docker-internal이 아닌 다른 사설 대역)으로 나가는
트래픽은 반드시 netgate를 거치고, 그때 비로소 차단 규칙이 적용됩니다.

## 아키텍처

```
호스트 netns
   │
   ▼
code-docker-netinit-docker (network_mode: host, NET_ADMIN+SYS_ADMIN - 라벨로 대상을
   발견해 nsenter로 각 대상의 netns에 들어가 기본 라우트를 지속적으로 재적용;
   대상 컨테이너는 이 capability를 하나도 갖지 않음)
   │
   ▼
code-docker (code-docker-internal 전용, NET_ADMIN 없음)
   │  netinit-docker가 지속적으로 심어주는 라우트로 default gw = router
   ▼
code-docker-router (code-docker-internal + code-docker-external 양쪽)
   │  - ip_forward + MASQUERADE + FORWARD 순서 있는 allow/block 룰(RFC1918 등)
   │  - dnsmasq가 DNS 리졸빙 + addn-hosts 기반 도메인 블록리스트를 겸함
   ▼
code-docker-external → 인터넷

호스트:80 → code-docker-router 자신의 nginx (직접 종단) → code-docker:80 (nginx, 리버스 프록시)

dind는 이미 privileged라 별도 에이전트 없이 자기 netns 안에서 같은 루프를 스스로 돎
```

- `config/netgate/config.default.yaml` - CIDR allow/block 순서 리스트(`outbound:`)와
  포트포워딩(`forwards:`)을 선언하는 설정 파일. `config/netgate/firewall.default.sh`가
  30초마다 이 파일을 읽어 iptables 규칙으로 변환합니다. 순서가 중요합니다 - iptables
  체인은 first-match-wins이므로, 구체적인 예외를 넓은 차단보다 먼저 배치해야 합니다.
- 기본 `outbound:` 값은 RFC1918(`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) +
  링크로컬(`169.254.0.0/16`) + 루프백(`127.0.0.0/8`)을 차단합니다.
- 기본 `forwards:` 값은 **비어 있습니다**(`forwards: []`). 예전에는 호스트 80번 포트를
  netgate의 PREROUTING DNAT로 `code-docker:80`에 전달했지만, 지금은 `code-docker-router`
  자신의 nginx가 80번을 직접 리스닝해 로컬 리스너로 처리하므로(로컬 리스너가 항상
  PREROUTING DNAT보다 우선합니다) 그 항목은 죽은 설정이 되어 제거되었습니다. `forwards:`
  항목을 추가하면(다른 호스트 포트를 다른 `code-docker-internal` 컨테이너로 전달하고
  싶을 때) 그 포트포워딩용 ACCEPT 규칙은 항상 RFC1918 차단 규칙보다 **먼저** 적용됩니다 -
  대상 컨테이너의 IP 자체가 RFC1918 대역에 속하기 때문입니다.
- code-docker/dind는 `/etc/resolv.conf`가 router를 가리키도록 설정되어 있고, router의
  `dnsmasq`(`config/dns/`)가 이 DNS 쿼리를 받아 자기 자신의(정상 동작하는)
  upstream으로 포워딩합니다. 같은 dnsmasq가 `addn-hosts=`로 StevenBlack/hosts 기반
  블록리스트 파일을 읽어, 리스트에 있는 도메인은 `0.0.0.0`으로 응답합니다 - DNS
  단계에서 이미 막히므로 어떤 TCP/TLS 연결도 시도되지 않습니다. (이전에는 squid의
  `REDIRECT`+`ssl_bump peek`로 HTTP(S) 트래픽을 가로채 `dstdomain`/SNI 기준으로
  차단했으나, squid의 anti-spoofing 체크가 IP 풀이 로테이션되는 CDN형 도메인(예:
  `registry-1.docker.io`)에서 오탐해 `docker pull`을 깨뜨리는 문제가 있어 DNS 레벨
  차단으로 교체했습니다 - 자세한 경위는
  [`.claude/router-dns-plan.md`](../.claude/router-dns-plan.md) 참고.)

## 위험한 패턴 - 새 브리징 컨테이너를 즉흥적으로 추가하지 마세요

`code-docker-internal`과 `code-docker-external`(또는 호스트) 양쪽에 붙는 컨테이너는 그
자체로 `router`/dind와 동급의 신뢰 레벨을 가집니다. "외부 Caddy가 code-docker에 못
닿으니 중간에 프록시 컨테이너 하나 두자"는 식으로 `(외부 Caddy) → (새 브리징 컨테이너)
→ code-docker` 패턴을 즉흥적으로 추가하면, 이 락다운을 완전히 우회하는 새 구멍(그
브리징 컨테이너를 통해 나가는 길)이 생길 수 있습니다. **이런 요구가 생기면 netgate
자체를 확장하거나(예: `config/netgate/config.default.yaml`에 forwards 항목 추가) 기존
nginx/[Dev Proxy](dev-proxy.md) 메커니즘을 쓰세요 - 새 브리징 컨테이너를 추가하지
마세요.**

## 이 시스템으로 못 막는 것

두 가지 서로 다른 "이 시스템으로 못 막는 구멍"이 있고, 성격이 다르므로 구분합니다.

1. **사용자가 직접 새 경로를 여는 경우** - `code-docker-external`을 code-docker에 직접
   다시 붙이거나, 위처럼 브리징 컨테이너를 추가하는 경우. dind는 예상치 못한 추가 기본
   게이트웨이를 발견하면 로그에 경고를 남기지만(`docker compose logs`), 자동으로
   되돌리지는 않습니다 - 사용자의 의도적인 변경일 수도 있기 때문입니다. code-docker
   쪽은 이제 `code-docker-netinit-docker`가 매 재조정 주기마다 라벨이 선언한 게이트웨이로
   라우트를 다시 덮어쓰므로, 수동으로 다른 기본 게이트웨이를 심어도 다음 주기에 원래대로
   되돌아갑니다 - "감지만 하고 안 되돌린다"가 아니라 "매번 원하는 상태로 재적용한다"는
   더 강한 보장입니다.
2. **정상적으로 허용된 아웃바운드 연결이 그 자체로 우회 통로가 되는 경우** - 예를 들어
   어떤 정상 SaaS API가 요청 파라미터에 따라 다른 목적지로 릴레이/fetch 해주는 기능이
   있다면, 그 요청 자체는 netgate 입장에서 완전히 정상 트래픽이라 막을 방법이 없습니다.
   이건 code-docker 경계를 벗어난 원격 서버에서 일어나는 confused-deputy 패턴이라
   **네트워크 레이어의 어떤 통제로도 원천적으로 해결 불가능**합니다 - 사용자가 인지하고
   있어야 하는 한계로 명시합니다.

블록리스트(dnsmasq)는 애초에 "강제적으로 완벽히 막는다"가 목적이 아니라 프롬프트 인젝션
콘텐츠 오염에 대한 1차/best-effort 방어입니다 - 실제 강제 방어는 CIDR 차단(FORWARD
체인)이 담당합니다. 내부자(오염된 에이전트)가 작정하고 우회하려면(IP 직접 지정, 리스트에
없는 새 도메인 등) 얼마든지 우회 가능합니다.

## 대역폭 제한 (네트워크 소진 공격 방어)

`outbound:`/`forwards:`는 트래픽이 **어디로** 가는지를 통제하고, `bandwidth:`는 **얼마나
빠르게** 나가는지를 통제합니다 - 목적지 기준 방어(CIDR allow/block)와는 별개로, code-docker
안에서 실행되는 무언가(오염된 에이전트 포함)가 대량의 아웃바운드 트래픽을 만들어 이
컨테이너가 물려있는 네트워크/회선 대역폭을 소진시키는 공격을 막기 위한 기능입니다.

router의 `netgate-shaping` supervisord 프로그램(`config/netgate/shaping.default.sh`)이
`netgate-firewall`과 별도로, 30초마다 `config.yaml`의 `bandwidth:` 섹션을 읽어 기본
인터페이스(`code-docker-external` 쪽) 위에 `tc`(Linux traffic control) HTB 큐잉
디시플린을 다시 적용합니다. 이 인터페이스로 나가는 **모든** 패킷(코드docker/dind에서
FORWARD되는 트래픽뿐 아니라 router 자기 자신이 만드는 트래픽까지)이 이 큐잉 규칙의
적용을 받습니다 - `outbound:`가 netfilter FORWARD 체인만 보는 것과 다른 지점입니다.

- `total_mbps` - 이 라우터가 내보내는 **전체** 트래픽 총합에 대한 하드 리밋(0 = 무제한).
- `services[]` - `target_host`(forwards의 `target_host`와 동일하게 해석 - hostname을
  `getent`로 resolve)별 **독립적인** 하드 리밋. `total_mbps`나 다른 서비스로부터
  빌려오지 않습니다 - 예를 들어 `code-docker`에 50Mbps, `dind`에 50Mbps를 설정하면
  둘 다 여유가 남아도 서로의 몫을 나눠쓰지 않고 각자 정확히 50Mbps에서 막힙니다.

같은 서브넷 안(code-docker↔dind, code-docker↔router)의 트래픽은 위 "같은 서브넷은
게이트웨이를 거치지 않는다" 절과 동일한 이유로 이 큐잉 규칙도 거치지 않습니다 - 오직
router의 기본 인터페이스를 실제로 통과하는(즉 외부로 나가는) 트래픽만 대상입니다.

## 예전에 있었던 함정 (moby/moby#50326) - 2026-08-25 구조적으로 해소됨

Docker 엔진에는 알려진 이슈([moby/moby#50326](https://github.com/moby/moby/issues/50326))가
있습니다: 어떤 컨테이너가 network namespace의 "소유주"인 상태에서 그 컨테이너가 단순
재시작이 아니라 **재생성**(다른 컨테이너 ID로 다시 만들어짐)되면, 그 네트워크
네임스페이스도 통째로 새로 만들어집니다.

2026-08-25 이전에는 `code-docker-netinit`이 `network_mode: service:code-docker`로
code-docker의 네트워크 네임스페이스를 공유했으므로 - 즉 code-docker가 그 네임스페이스의
"소유주"였으므로 - 이 이슈에 정면으로 걸렸습니다: code-docker가 재생성되면 이미
붙어있던 `code-docker-netinit`은 옛(이제는 죽은) 네임스페이스에 고아로 남아 라우트를
전혀 심을 수 없는 상태가 됐습니다. `restart: unless-stopped`가 죽은 컨테이너 ID를
상대로 재시도만 반복할 뿐, 새 네임스페이스에 다시 합류하지는 못했습니다(Compose가
`network_mode: service:<target>`을 대상의 **컨테이너 ID**로 고정해서 저장하기
때문). 이 클래스의 장애가 2026-08-25 하루에만 실제로 두 번 관찰됐습니다(한 번은
roblox-studio-docker의 `studio-netinit`, 한 번은 이 네트워크에 라벨을 적용하려고
재생성하는 과정에서 `code-docker-netinit` 자신) - 자세한 경위는 code-docker 저장소의
`.claude/archive/netinit-docker-plan-done.md`(그 저장소에 있음, 여기 아님)의 "발단" 절 참고.

**지금은 이 문제 자체가 구조적으로 없어졌습니다.** `code-docker-netinit` 사이드카는
제거됐고, 그 역할은 `code-docker-netinit-docker`(호스트 netns에서 도는 라벨 기반 에이전트,
위 "아키텍처" 절 참고)가 대신합니다. 이 에이전트는 대상의 네트워크 네임스페이스를
공유하지 않습니다 - `docker.sock`으로 매 재조정 주기(reconcile cycle)마다 대상의 **현재**
`SandboxKey`를 다시 조회해서 `nsenter`로 잠깐 들어갔다 나올 뿐, 컨테이너 ID나 네임스페이스
핸들을 어디에도 붙잡아두지 않습니다. 즉 moby/moby#50326이 만드는 상황(대상 재생성 → 새
네임스페이스) 자체는 여전히 일어나지만, 에이전트가 그 사건을 특별히 감지하거나 복구할
필요조차 없습니다 - 다음 주기에 그냥 새 `SandboxKey`를 읽어올 뿐입니다.
`docker compose up -d --force-recreate <대상>` 후 수동 개입 없이 라우트가 돌아오는 것이
이 설계의 회귀 테스트이고, 2026-08-25에 실측으로 통과를 확인했습니다(위
`netinit-docker-plan.md`의 "실측 검증 결과" 절 참고).

옛 사이드카 체제가 쓰던 자기 진단(로컬 netns에 loopback 외 인터페이스가 하나도 없으면
고아 상태로 판단해 `exit 1`로 스스로 종료 → `restart: unless-stopped`가 재생성해서 새
네임스페이스에 재합류) 절차나, 재부팅 후 `docker compose ps`로 사이드카가 정상적으로 떠
있는지 확인하는 습관은 더 이상 필요하지 않습니다 - 애초에 그런 고아 상태 자체가
일어날 수 없는 구조입니다.

## 운영상 알려진 함정 - Docker의 `DOCKER-INTERNAL` 강제 격리 (Docker Engine 29.x+)

**증상**: `docker compose up`이 정상적으로 끝나고 `code-docker-netinit-docker`/router 모두
멀쩡히 떠 있는데도, code-docker 안에서 인터넷으로 나가는 모든 연결(code-server의
GitHub 버전 체크, `user-init.sh`의 qwreey-fish curl 등)이 계속 타임아웃납니다.
`getent hosts <domain>`은 성공하는데(Docker 내장 DNS `127.0.0.11`이 도커 데몬 쪽에서
대신 응답해주는 경로라 code-docker 자신의 네트워크 경로를 전혀 검증하지 않습니다 -
착시입니다) 실제 TCP 연결은 전부 안 됩니다. router 컨테이너 자신의 `curl`은 정상 동작하고,
router 안 netgate의 iptables(FORWARD/MASQUERADE)도 전부 정상으로 보입니다 - 즉
router 안에서는 아무 이상이 안 보이는데도 안 됩니다.

**원인**: 최신 Docker Engine(29.5.2에서 확인됨 - 이 프로젝트를 원래 만들 때 쓰던 버전에는
없던 동작)은 `internal: true`로 선언된 네트워크에 대해 호스트의 nftables/iptables에
`DOCKER-INTERNAL`이라는 체인을 추가로 깔아둡니다. 이 체인은 "그 네트워크의 브리지에서
들어온 패킷인데 목적지가 그 브리지 자신의 서브넷 밖이면 무조건 DROP"이라는 규칙을
강제합니다 - `internal: true` 네트워크가 그 어떤 경로로도 Docker 관리 밖(인터넷)으로
못 나가게 하려는 Docker 자체의 하드닝입니다. 그런데 이 락다운 문서의 라우팅 설계는
**정확히 그 반대를 의도적으로** 합니다: router가 `code-docker-internal`과
`code-docker-external` 양쪽에 다리를 걸치고 자기 자신의 iptables로 트래픽을 걸러서
내보내주는 것 자체가 이 기능의 핵심이거든요. `DOCKER-INTERNAL`은 호스트 레벨에서
router의 forwarding 여부와 무관하게 무조건 앞단에서 패킷을 죽여버리므로, router 안의
netgate 설정이 아무리 정상이어도 절대 성공할 수 없습니다. `nft list ruleset`으로
호스트에서 직접 확인하면 이 체인에 실제로 드롭된 패킷 카운터가 쌓여있는 걸 볼 수
있습니다.

**대응**: Docker는 정확히 이런 오버라이드를 위해 `DOCKER-USER`라는 빈 체인을 항상
남겨두고, 자기 자신의 `DOCKER-FORWARD`/`DOCKER-INTERNAL`보다 먼저 평가되게 해뒀습니다.
`code-docker-netinit-docker`라는 전용 컨테이너(2026-08-25 이전 이름은
`code-docker-netfilter-fix` - qwreey/router-docker-client 저장소 안에서도 `netfilter-fix/`
서브디렉터리가 `netinit-docker/`로, `fix.sh`가 `netinit-docker.sh`로 그대로 개명됐습니다.
`router/`/`code-dind`처럼 이 저장소의 git submodule은 아니고, `netinit`과 마찬가지로
그 저장소에서 직접 받아오는 원격 git 빌드 컨텍스트입니다 - 위 "아키텍처" 절과 code-docker의
`docker-compose.yml` 참고)가 `docker-compose.yml`에 포함되어 있고, `docker compose up`만으로
자동으로 같이 떠서 이 문제를 해결합니다 - 별도 설치 단계가 없습니다. 이 컨테이너는 이제 이
DOCKER-USER 예외 작업 하나만 하지 않습니다 - 위 "아키텍처"/"예전에 있었던 함정" 절에서 설명한,
라벨로 opt-in한 컨테이너들에 기본 라우트를 심는 작업까지 같은 재조정 루프 하나가
겸합니다(개명한 이유이기도 합니다).

이 컨테이너는 `network_mode: host`(어떤 docker 네트워크에도 붙지 않습니다) +
`NET_ADMIN`/`SYS_ADMIN` + 읽기 전용 `docker.sock` 마운트 + `/var/run/docker/netns`
마운트(`ro,rslave` - 대상 netns에 `nsenter`로 들어가기 위한 것, 새로 생긴 대상에서도
동작하려면 `rslave` 전파가 필수입니다)가 필요합니다 - 컨테이너의 `NET_ADMIN`은 기본적으로
자기 자신의 네트워크 네임스페이스 안에서만 관리자 권한을 주므로, 호스트 자체의 netfilter
테이블을 건드리거나 다른 컨테이너의 netns에 `setns()`하려면 호스트의 네트워크
네임스페이스 자체를 공유해야 하기 때문입니다. 이건 이 저장소의 다른 서비스들보다 눈에
띄게 큰 신뢰 등급이지만, `code-docker-dind`가 이미 privileged + 인증 없는 도커 소켓으로
동작하고 있어서(위 `code-docker-dind`의 주석 참고) `code-docker-internal`에 닿을 수 있는
사람은 이미 호스트 커널과 동급의 접근권을 갖는다는 전제가 이 프로젝트에 이미 있습니다 -
그 전제 위에 새로운 위험 등급을 추가하는 게 아닙니다. `docker.sock`을 `:ro`로 마운트하는
것도 완화책이 아니라는 점을 명확히 해둡니다 - `:ro`는 소켓 파일(inode) 자체에만 적용되고
그 소켓을 통해 무엇을 쓸 수 있는지와는 무관해서, `:ro`로 마운트된 소켓으로도 컨테이너
생성 같은 호출은 그대로 성공합니다(2026-08-25 실측 확인). 그래도 blast radius를 좁혀두려고
기존 서비스(code-docker/router)에 얹지 않고 이 한 가지 일만 하는 별도의 최소
이미지(`netinit-docker/`)로 분리했습니다.

호스트 systemd 유닛이 아니라 compose 서비스로 만든 이유는 두 가지입니다: (1)
`docker compose down` 시 이 컨테이너도 같이 내려가면서 자기가 넣은 `DOCKER-USER`
규칙을 SIGTERM 핸들러에서 스스로 지우고 종료합니다(`netinit-docker/netinit-docker.sh`) -
스택이 내려가 있는 동안 필요 없는 예외 규칙이 호스트에 계속 남아있지 않습니다. (2) 여러
`PREFIX` 인스턴스를 한 호스트에서 돌릴 때, compose 서비스는 인스턴스마다 자동으로
하나씩 따라오지만 systemd 유닛은 인스턴스마다 별도로 설치/enable해야 해서 깜빡하기
쉽습니다.

`code-docker-internal` 네트워크의 브리지 이름(`br-<네트워크 ID 앞 12자>`)은
`docker compose down`/`up`으로 네트워크가 재생성될 때마다 바뀌므로,
`netinit-docker/netinit-docker.sh`는 주기적으로 현재 살아있는 브리지 이름을 도커 소켓으로
다시 조회해서 규칙을 맞추고, 예전 브리지 이름으로 남은 낡은 규칙은 지웁니다
(`config/netgate/firewall.default.sh`가 router 컨테이너 안에서 쓰는 것과
같은 재조정 루프 방식 - 같은 루프가 이제 라우트 재적용도 겸합니다). `iptables` 호환
레이어 대신 `nft`를 직접 사용해서, 도커 데몬이 실제로 관리하는 것과 동일한 nftables
오브젝트를 백엔드 불일치 없이 확실하게 건드립니다.

router가 `code-docker-internal` 외에 다른 `internal: true` 네트워크에도 붙는 경우(예:
`EXTRA_INCLUDE`로 연동한 sibling 프로젝트가 router를 자기 전용 네트워크에도 붙이는 경우
- `.claude/archive/roblox-studio-vnc-isolation-plan-done.md` 참고) 그 네트워크도 똑같이
`DOCKER-INTERNAL`에 막힙니다. 예전에는 이걸 `NETFILTER_FIX_INTERNAL_NETWORK`/
`NETFILTER_FIX_EXTRA_INTERNAL_NETWORKS`라는 code-docker `.env` 값으로 알려줘야 했지만,
이제는 **네트워크 쪽이 라벨로 자기 요구를 직접 선언**합니다 - 그 sibling 프로젝트의
오버레이 파일에서:

```yaml
networks:
  roblox-studio-vnc:
    labels:
      netinit.provider: "${PREFIX:-}code-docker-netinit-docker"
      netinit.exempt-forward: "true"
```

이렇게 뒤집은 이유는 형제 프로젝트를 하나 붙일 때마다 code-docker 쪽 `.env`를 고쳐야
하는 결합을 없애기 위해서입니다 - `EXTRA_INCLUDE`가 애초에 없애려던 바로 그 결합입니다.
`NETFILTER_FIX_INTERNAL_NETWORK`/`NETFILTER_FIX_EXTRA_INTERNAL_NETWORKS`는 **deprecated**로
남아 최소 한 주기는 계속 동작하고(쓰이면 경고 로그를 남깁니다), `code-docker-internal`
자신의 기본 보호는 그 네트워크 자신의 `netinit.exempt-forward` 라벨로 항상 그대로
유지됩니다 - 자세한 내용은 code-docker의 `example-env`와 `.claude/archive/netinit-docker-plan-done.md`
참고.

## 당장 인터넷이 필요하다면 (기능 자체를 끄기)

`NETGATE_ENABLED="false"`(`.env`)로 끄면 dind 자신의 라우팅 루프, code-docker 시작 시의
라우트 대기 가드, `code-docker-router` 자신의 방화벽(CIDR 차단/인바운드 포트포워딩) 적용
루프, 그리고 `code-docker-netinit-docker`의 라우트 심기가 전부 아무것도 안 하고 idle
상태가 됩니다. 마지막 것은 code-docker의 `docker-compose.yml`이 이 값을 그 컨테이너
자신의 `NETINIT_DOCKER_ENABLED`로 넘겨주기 때문입니다 - netinit-docker는
router-docker-client의 범용 도구라 env 이름이 자기 자신을 가리키고, 사용자 쪽 스위치
하나(`NETGATE_ENABLED`)에 그걸 매핑하는 건 code-docker의 compose 파일입니다.

단, netinit-docker에서 꺼지는 건 **라우트 심기 절반뿐**입니다 - DOCKER-USER 예외는
애초에 netgate가 켜져 있는지와 무관한 별개 작업이라 계속 유지됩니다(`internal: true`
네트워크에 대한 Docker 자신의 하드닝을 되돌리는 것이지, egress 경계를 세우는 게
아닙니다). `code-docker-dind`는 이 라벨을 달고 있지 않으므로 애초에 해당 없음 -
privileged라 처음부터 자기 루프로 직접 관리합니다.

`NETGATE_ENABLED="false"`는 그 나머지(dind/router 쪽 루프)에 대해서는
`TAILSCALE_ENABLED`와 같은 패턴으로, router 컨테이너 자체나 DNS 포워딩/tailscale/
Dev Proxy/tinyauth 같은 다른 기능은 계속 정상 동작합니다 (이전에는 router 컨테이너
전체가 idle 상태가 되어 이 기능들까지 같이 죽는 버그가 있었습니다). **다만
이것만으로는 예전(제한 없음) 토폴로지로 완전히 돌아가지는 않습니다** -
`code-docker-external`이 이미 code-docker/dind의 `networks:`에서 빠져 있고, `ports:
- 80:80`도 code-docker가 아니라 router 서비스에 있어서, `NETGATE_ENABLED=false`만으로는
code-docker 자신이 여전히 인터넷/호스트에 직접 나갈 인터페이스가 없습니다. Compose는
네트워크 attachment/포트 퍼블리시 여부를 런타임 환경변수로 조건부 처리할 수 없기 때문에,
완전히 예전 토폴로지로 되돌리려면 `docker-compose.yml`을 직접 수정해야 합니다:

- `code-docker`, `code-docker-dind` 두 서비스의 `networks:`에
  `code-docker-external: {}`를 다시 추가하세요.
- `code-docker`의 `ports:`에서 주석 처리된 `- 22:22`를 다시 살리고, `- 80:80`도
  추가하세요(원래 code-docker에 있던 포트입니다 - 지금은 `code-docker-router`
  서비스의 `ports:`에 있습니다, 그건 그대로 둬도 되고 지워도 됩니다).
- `NETGATE_ENABLED="false"`도 같이 설정해 두면 code-docker와 dind 둘 다 굳이 존재하는
  `router`를 거칠 필요 없이 바로 나갈 수 있습니다 (code-docker 쪽은 이 값이
  `code-docker-netinit-docker`의 `NETINIT_DOCKER_ENABLED`로 전달돼 라우트를 심지 않게
  되고, dind 쪽은 자기 루프가 idle이 됩니다)(router 자체를 compose에서 완전히 빼는 것도 가능하지만,
  그건 tailscale/Dev Proxy까지 같이 잃는다는 뜻이라 이 문서 범위 밖의 더 큰 수술입니다 -
  router.md 참고).

이건 `DIND_TARGET=dind`로 dind-authz 보호를 완전히 끄는 것과 같은 성격의, 의도적으로
눈에 띄는 수동 작업입니다.

## 설정 커스터마이징

`outbound:`(CIDR allow/block 순서 리스트)와 `forwards:`(포트포워딩)는 이제 재빌드 없이
**`/router/` 페이지(또는 webmanager)의 "Net 관리" 탭**에서 직접 관리할 수 있습니다 -
DNS 탭이 도입한 것과 같은 "라이브 카피" 방식으로, router-manager가 처음 뜰 때
`config.override.yaml`(있으면) 또는 `config.default.yaml`을 `/var/lib/code-docker-router/netgate/config.yaml`에
한 번만 복사해 오고, 그 이후로는 이 라이브 카피가 유일한 진실 소스입니다 -
`firewall.default.sh`도 이 라이브 카피를 우선 읽습니다. 웹에서 바꾼 값은 이미지
업데이트로 `config.default.yaml`이 바뀌어도 절대 덮어써지지 않습니다.

**Net 관리 탭에는 중요한 한계가 명시되어 있습니다**: `code-docker-internal`에 직접 붙은
컨테이너끼리(code-docker↔dind, code-docker↔router)의 트래픽은 커넥티드 라우트를 타고
FORWARD 체인 자체를 거치지 않으므로, 이 outbound 규칙으로 절대 제어할 수 없습니다 - 이
탭의 로직에도 그 사실이 경고 배너로 노출됩니다.

같은 "Net 관리" 탭 안, outbound/forwards 아래에 **대역폭 제한** 카드가 있습니다 - 위
"대역폭 제한" 절에서 설명한 `bandwidth.total_mbps`/`bandwidth.services[]`를 여기서
편집합니다(`GET`/`PUT /api/netgate/bandwidth`). 같은 라이브 카피/30초 반영 방식을
그대로 씁니다.

파일로 직접 다루고 싶다면 여전히 가능합니다: `config/netgate/config.default.yaml`을
참고해서 `config/netgate/config.override.yaml`을 만들면(override 패턴,
`docker compose build code-docker-router && docker compose up -d` 필요) 되지만, 이는
router-manager가 라이브 카피를 아직 만들지 않은 **최초 1회**에만 적용되고 그 이후로는
무시됩니다(라이브 카피가 이미 있으면 그쪽이 항상 우선) - 이미 웹으로 관리 중인 배포에는
효과가 없습니다.

DNS 블록리스트/추가 호스트/리졸버는 이제 재빌드 없이 **`/router/` 페이지의 DNS
탭**(또는 webmanager의 DNS 탭)에서 직접 관리할 수 있습니다 - 사용자 블록리스트를
여러 개 만들 수 있고(각각 이름 + 호스트 목록), 특정 호스트 이름을 실제 IP로 매핑하는
"추가 호스트"(MagicDNS와 비슷한 개념), 업스트림 DNS 서버를 `1.1.1.1` 같은 값으로
고정하는 리졸버 설정도 여기서 바꿉니다. 이미지에 내장된 StevenBlack/hosts 블록리스트가
새 버전으로 갱신되면 DNS 탭에 배지가 뜨고, 추가/제거된 호스트를 확인한 뒤 가져올지
지금 목록을 유지할지 고를 수 있습니다 - 자세한 설계는
`.claude/dns-blocklist-management-plan.md` 참고.

파일로 직접 다루고 싶다면 여전히 가능합니다: `config/dns/blocklist.override.hosts`
(hosts 포맷, 예: `0.0.0.0 evil.com`)를 두면 재빌드 시 이미지 내장 StevenBlack/hosts
블록리스트 위에 **항상 추가로**(대체 아님, 예전부터 그랬던 동작 그대로) 얹힙니다 -
DNS 탭이 관리하는 소스들과는 별개의, 파일 기반 전용 경로입니다. dnsmasq가 원래
hosts 포맷을 그대로 읽으므로 별도 변환 스크립트는 필요 없습니다.

내장 StevenBlack/hosts 블록리스트 자체를 배포 단계에서 끄거나 완전히 다른
소스로 바꾸고 싶다면 `example-env.router`의 `DNS_BUILTIN_BLOCKLIST_ENABLED`
(`"false"`로 끄기)와 `DNS_BUILTIN_BLOCKLIST_SOURCE`(다른 hosts 파일 경로 -
docker-compose.yml에서 그 경로로 자신만의 파일을 바인드 마운트하는 용도)를
쓰세요. 둘 다 컨테이너 (재)생성이 필요하고 런타임에 웹 UI로는 바꿀 수 없습니다
- 이미지에 내장된 소스 자체를 무엇으로 볼지 정하는 배포 설정이라 의도적으로
그렇게 만들었습니다.
