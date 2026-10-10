// 지도 블록 (FR-716, D17 「지도」).
//
// 본문의 지도 블록 `<img data-map="kakao|naver|google" data-lat data-lng data-zoom data-label>` 을 그
// 지도사의 공식 SDK 로 그린 지도로 바꾼다. 키는 이 스크립트 태그의 data-* 에서 온다(A-201) — 각 콘솔에
// 등록한 도메인에서만 동작하는 공개값이다. 키가 없거나 SDK 를 못 부르면 자리표시 그림을 그대로 둔다.
// 에디터(editor.js)는 OndolithMaps.pick 으로 위치를 고르고 OndolithMaps.html 로 블록을 넣는다.
//
// SDK 사용법은 2026-10-10 공식 문서로 확인했다 — 카카오 apis.map.kakao.com/web/guide,
// 네이버 navermaps.github.io/maps.js.ncp, 구글 developers.google.com/maps/documentation/javascript.
(function () {
  'use strict';

  var me = document.currentScript;
  var keys = {
    naver: me.dataset.naver || '', kakao: me.dataset.kakao || '',
    google: me.dataset.google || '', googleMapId: me.dataset.googleMapId || '',
  };
  var LABELS = { kakao: '카카오맵', naver: '네이버 지도', google: '구글 지도' };
  // 카카오는 level(작을수록 가깝다), 네이버·구글은 zoom(클수록 가깝다). 블록에는 그 지도사의 값을 그대로 둔다.
  var DEFAULT_ZOOM = { kakao: 3, naver: 16, google: 16 };
  var SEOUL_CITY_HALL = { lat: 37.5665, lng: 126.978 };

  // 그릴 수 있는 지도사. 구글은 키와 Map ID 가 다 있어야 한다 — 핀(AdvancedMarkerElement)이 Map ID 를 요구한다.
  function providers() {
    return ['kakao', 'naver', 'google'].filter(function (p) {
      return p === 'google' ? keys.google && keys.googleMapId : keys[p];
    });
  }

  function script(src) {
    return new Promise(function (ok, no) {
      var s = document.createElement('script');
      s.src = src;
      s.async = true;
      s.onload = ok;
      s.onerror = function () { no(new Error('불러오지 못했다: ' + src)); };
      document.head.appendChild(s);
    });
  }

  // 지도사마다 SDK 를 한 번만 부른다.
  var loading = {};
  function load(p) {
    if (loading[p]) return loading[p];
    var k = encodeURIComponent(keys[p]);
    if (p === 'kakao') {
      // autoload=false 면 kakao.maps.load 가 준비를 알린다. 장소 검색은 services 라이브러리다.
      loading[p] = script('https://dapi.kakao.com/v2/maps/sdk.js?appkey=' + k + '&libraries=services&autoload=false')
        .then(function () { return new Promise(function (ok) { kakao.maps.load(ok); }); });
    } else if (p === 'naver') {
      // 주소 검색은 geocoder 서브모듈이다. 인증이 실패하면 SDK 가 navermap_authFailure 를 부른다.
      loading[p] = new Promise(function (ok, no) {
        window.navermap_authFailure = function () { no(new Error('네이버 지도 인증 실패 — ncpKeyId 와 등록한 서비스 URL 을 확인하세요')); };
        script('https://oapi.map.naver.com/openapi/v3/maps.js?ncpKeyId=' + k + '&submodules=geocoder').then(ok, no);
      });
    } else {
      // loading=async 면 callback 이 준비를 알리고, 라이브러리는 importLibrary 로 부른다.
      loading[p] = new Promise(function (ok, no) {
        window.__ondolithGoogleMaps = ok;
        script('https://maps.googleapis.com/maps/api/js?key=' + k + '&loading=async&callback=__ondolithGoogleMaps').catch(no);
      }).then(function () {
        return Promise.all(['maps', 'marker', 'geocoding'].map(function (l) { return google.maps.importLibrary(l); }));
      });
    }
    loading[p].catch(function () { delete loading[p]; });
    return loading[p];
  }

  // draw makes a map with one pin in el and returns a handle that hides how each provider does it:
  // move(lat, lng) · zoom() · search(q) → {lat, lng, label}|null · onClick(lat, lng) (덮어쓴다).
  function draw(p, el, lat, lng, zoom) {
    return load(p).then(function () {
      var h = { onClick: function () {} };
      if (p === 'kakao') {
        var kc = new kakao.maps.LatLng(lat, lng);
        var kmap = new kakao.maps.Map(el, { center: kc, level: zoom });
        var kpin = new kakao.maps.Marker({ position: kc, map: kmap });
        kakao.maps.event.addListener(kmap, 'click', function (e) { h.onClick(e.latLng.getLat(), e.latLng.getLng()); });
        h.move = function (a, b) { var q = new kakao.maps.LatLng(a, b); kpin.setPosition(q); kmap.setCenter(q); };
        h.zoom = function () { return kmap.getLevel(); };
        h.search = function (q) {
          return new Promise(function (ok) {
            new kakao.maps.services.Places().keywordSearch(q, function (data, status) {
              var d = status === kakao.maps.services.Status.OK && data[0];
              ok(d ? { lat: +d.y, lng: +d.x, label: d.place_name } : null);
            });
          });
        };
      } else if (p === 'naver') {
        var nc = new naver.maps.LatLng(lat, lng);
        var nmap = new naver.maps.Map(el, { center: nc, zoom: zoom });
        var npin = new naver.maps.Marker({ position: nc, map: nmap });
        naver.maps.Event.addListener(nmap, 'click', function (e) { h.onClick(e.coord.lat(), e.coord.lng()); });
        h.move = function (a, b) { var q = new naver.maps.LatLng(a, b); npin.setPosition(q); nmap.setCenter(q); };
        h.zoom = function () { return nmap.getZoom(); };
        h.search = function (q) {
          return new Promise(function (ok) {
            naver.maps.Service.geocode({ query: q }, function (status, res) {
              var d = status === naver.maps.Service.Status.OK && res.v2.addresses[0];
              ok(d ? { lat: +d.y, lng: +d.x, label: d.roadAddress || d.jibunAddress } : null);
            });
          });
        };
      } else {
        var gmap = new google.maps.Map(el, { center: { lat: lat, lng: lng }, zoom: zoom, mapId: keys.googleMapId });
        var gpin = new google.maps.marker.AdvancedMarkerElement({ map: gmap, position: { lat: lat, lng: lng } });
        gmap.addListener('click', function (e) { if (e.latLng) h.onClick(e.latLng.lat(), e.latLng.lng()); });
        h.move = function (a, b) { gpin.position = { lat: a, lng: b }; gmap.setCenter({ lat: a, lng: b }); };
        h.zoom = function () { return gmap.getZoom(); };
        h.search = function (q) {
          return new google.maps.Geocoder().geocode({ address: q }).then(function (r) {
            var d = r.results[0];
            return d ? { lat: d.geometry.location.lat(), lng: d.geometry.location.lng(), label: d.formatted_address } : null;
          }, function () { return null; });
        };
      }
      return h;
    });
  }

  function num(v, lo, hi) {
    var n = parseFloat(v);
    return isFinite(n) && n >= lo && n <= hi ? n : null;
  }

  // render replaces each map block under root with a live map. 에디터 안의 블록은 편집 중이라 두고,
  // SDK 를 못 부르면 자리표시를 되돌린다.
  function render(root) {
    (root || document).querySelectorAll('img[data-map]').forEach(function (img) {
      var p = img.getAttribute('data-map');
      var lat = num(img.dataset.lat, -90, 90), lng = num(img.dataset.lng, -180, 180);
      if (providers().indexOf(p) < 0 || lat === null || lng === null || img.closest('.sun-editor')) return;
      var holder = img.closest('figure') || img;
      var box = document.createElement('div');
      box.className = 'map-view';
      var area = document.createElement('div');
      area.className = 'map-area';
      area.style.cssText = 'width:100%;height:360px';
      box.appendChild(area);
      if (img.dataset.label) {
        var cap = document.createElement('p');
        cap.className = 'map-caption';
        cap.textContent = img.dataset.label;
        box.appendChild(cap);
      }
      holder.replaceWith(box); // SDK 는 크기가 있는 상자에 그린다 — 먼저 자리를 바꾼다
      draw(p, area, lat, lng, parseInt(img.dataset.zoom, 10) || DEFAULT_ZOOM[p]).catch(function () {
        box.replaceWith(holder);
      });
    });
  }

  function attr(s) {
    return String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  // html is the block the editor inserts. 서버 정화(richtext.go)가 같은 모양을 본다: 이름은 「"<>」 없이
  // 100자, 대체 글은 UGC 정책이 받는 글자만(쌍점이 있으면 정책이 alt 를 통째로 지운다).
  function html(b) {
    var label = String(b.label || '').replace(/["<>]/g, '').trim().slice(0, 100);
    var alt = ('지도 ' + label).replace(/[^\p{L}\p{N}\s\-_',.[\]!/()&]/gu, '').trim();
    return '<img src="/static/img/map.svg" alt="' + attr(alt) + '" data-map="' + b.provider + '" data-lat="' +
      b.lat.toFixed(7) + '" data-lng="' + b.lng.toFixed(7) + '" data-zoom="' + (b.zoom | 0) + '"' +
      (label ? ' data-label="' + attr(label) + '"' : '') + '>';
  }

  // pick opens the map dialog and calls done({provider, lat, lng, zoom, label}) on 「넣기」.
  function pick(done) {
    var list = providers();
    var dlg = document.createElement('dialog');
    dlg.className = 'map-pick';
    dlg.setAttribute('aria-label', '지도 넣기');
    dlg.style.cssText = 'width:min(680px,94vw);padding:16px;border:1px solid #ccc;border-radius:8px';
    var row = 'display:flex;gap:8px;margin-bottom:8px;flex-wrap:wrap';
    dlg.innerHTML =
      '<div style="' + row + '"><label>지도 <select class="map-provider"></select></label></div>' +
      '<form class="map-search" style="' + row + '"><input type="search" class="map-query" placeholder="장소·주소 검색" style="flex:1;min-width:12em">' +
      '<button type="submit">검색</button></form>' +
      '<p class="map-hint" style="margin:0 0 8px;font-size:.9em">지도를 누르면 핀이 그 자리로 옮겨 갑니다.</p>' +
      '<div class="map-area" style="width:100%;height:320px;background:#eef2f0"></div>' +
      '<div style="' + row + ';margin-top:8px"><label style="flex:1">이름 <input type="text" class="map-label" maxlength="100" style="width:100%"></label></div>' +
      '<div style="display:flex;gap:8px;justify-content:flex-end"><button type="button" class="map-cancel">취소</button>' +
      '<button type="button" class="map-ok">넣기</button></div>';
    var sel = dlg.querySelector('.map-provider');
    list.forEach(function (p) {
      var o = document.createElement('option');
      o.value = p;
      o.textContent = LABELS[p];
      sel.appendChild(o);
    });
    var area = dlg.querySelector('.map-area'), hint = dlg.querySelector('.map-hint');
    var state = { lat: SEOUL_CITY_HALL.lat, lng: SEOUL_CITY_HALL.lng }, handle = null;
    function show() {
      var p = sel.value;
      area.innerHTML = '';
      handle = null;
      draw(p, area, state.lat, state.lng, DEFAULT_ZOOM[p]).then(function (h) {
        if (sel.value !== p) return;
        handle = h;
        h.onClick = function (a, b) { state.lat = a; state.lng = b; h.move(a, b); };
      }, function (e) {
        hint.textContent = LABELS[p] + '를 불러오지 못했습니다 — 키와 콘솔에 등록한 도메인을 확인하세요. (' + e.message + ')';
      });
    }
    sel.addEventListener('change', show);
    dlg.querySelector('.map-search').addEventListener('submit', function (e) {
      e.preventDefault();
      var q = dlg.querySelector('.map-query').value.trim();
      if (!q || !handle) return;
      handle.search(q).then(function (r) {
        if (!r) { hint.textContent = '「' + q + '」 를 찾지 못했습니다. 다른 말로 찾거나 지도를 눌러 고르세요.'; return; }
        state.lat = r.lat;
        state.lng = r.lng;
        handle.move(r.lat, r.lng);
        var lab = dlg.querySelector('.map-label');
        if (!lab.value) lab.value = r.label || q;
      });
    });
    function close() { dlg.close(); dlg.remove(); }
    dlg.querySelector('.map-cancel').addEventListener('click', close);
    dlg.querySelector('.map-ok').addEventListener('click', function () {
      if (!handle) return;
      done({ provider: sel.value, lat: state.lat, lng: state.lng, zoom: handle.zoom(), label: dlg.querySelector('.map-label').value });
      close();
    });
    document.body.appendChild(dlg);
    dlg.showModal();
    show();
  }

  window.OndolithMaps = { providers: providers, pick: pick, html: html, render: render };
  render(document);
})();
