// studio-city.js — 3D-город студии: год постов, один дом — один день. Код перенесён из макета docs/studio-city-mock.html.
// createCity(canvas, { onHover }) → { setData, setSource, highlight } или null, если WebGL недоступен.
// Кадр рисуется только когда что-то движется (камера догоняет цель, растут дома, идёт автовращение); цикл стоит на скрытой вкладке и вне экрана.
import * as THREE from 'three';

const W = 53, D = 7, CAP = W * D;   // колонки-недели × дни недели; дом — один день
const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
const HEAT = ['#1c2040', '#33307a', '#4a3bb8', '#6c50f0', '#a58bff'];
// цвета домов — те же токены --heat0..4, что у тепловой карты, чтобы город и сетка читались как одно
const LV = HEAT.map((fallback, i) => new THREE.Color(getComputedStyle(document.documentElement).getPropertyValue(`--heat${i}`).trim() || fallback));
const HOT = new THREE.Color('#ffffff');
const lvl = v => v === 0 ? 0 : v < 3 ? 1 : v < 5 ? 2 : v < 7 ? 3 : 4;
const height = v => .16 + v * 1.15;
const cellPos = k => [Math.floor(k / 7) - (W - 1) / 2, (k % 7) - (D - 1) / 2];
const PHI_MIN = .32, PHI_MAX = 1.42, SENS = .0042;
const LOOK0 = new THREE.Vector3(0, 4.6, 0);

export function createCity(canvas, { onHover = () => {} } = {}) {
    let renderer;
    try { renderer = new THREE.WebGLRenderer({ canvas, antialias: true, powerPreference: 'high-performance' }); }
    catch { return null; }
    renderer.setPixelRatio(Math.min(devicePixelRatio || 1, 2));
    renderer.shadowMap.enabled = true; renderer.shadowMap.type = THREE.PCFSoftShadowMap;
    renderer.toneMapping = THREE.NoToneMapping;
    renderer.setClearColor(0x000000, 0);
    // потеря контекста GPU: не глушим канвас, после восстановления перерисуем
    canvas.addEventListener('webglcontextlost', e => e.preventDefault());
    canvas.addEventListener('webglcontextrestored', () => { colorsDirty = true; kick(); });

    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(32, 2, .5, 400);
    scene.add(new THREE.AmbientLight(0xffffff, 1.35));
    const sun = new THREE.DirectionalLight(0xffffff, 1.6);
    sun.position.set(-22, 40, 26); sun.castShadow = true;
    sun.shadow.mapSize.set(2048, 2048); sun.shadow.bias = -.0005; sun.shadow.normalBias = .02;
    Object.assign(sun.shadow.camera, { left: -34, right: 34, top: 14, bottom: -14, near: 10, far: 110 });
    scene.add(sun);
    // невидимый пол, который только ловит тень
    const floor = new THREE.Mesh(new THREE.PlaneGeometry(W + 6, D + 6), new THREE.ShadowMaterial({ opacity: .35 }));
    floor.rotation.x = -Math.PI / 2; floor.receiveShadow = true; scene.add(floor);

    const barGeo = new THREE.BoxGeometry(1, 1, 1); barGeo.translate(0, .5, 0);
    const bars = new THREE.InstancedMesh(barGeo, new THREE.MeshLambertMaterial(), CAP);
    bars.castShadow = true; bars.receiveShadow = true; bars.count = 0;
    // сфера для луча задана заранее: высоты домов меняются, а авто-сфера устарела бы
    bars.boundingSphere = new THREE.Sphere(new THREE.Vector3(0, 2, 0), 60);
    for (let k = 0; k < CAP; k++) bars.setColorAt(k, LV[0]);
    scene.add(bars);
    const raycaster = new THREE.Raycaster();

    // данные: pool/published — по дням, N — число дней до сегодня включительно
    let pool = [], published = [], vals = [], N = 0, src = 'pool', hot = -1;
    const shown = new Float32Array(CAP);
    let intro = 0, colorsDirty = true;

    // камера-орбита: цели двигает ввод, фактические углы догоняют их (glide)
    const HOME = { theta: -.16, phi: .9, r: 66 };
    const rig = { theta: HOME.theta, phi: HOME.phi, r: HOME.r, tTheta: HOME.theta, tPhi: HOME.phi, tR: HOME.r, vTheta: 0, vPhi: 0 };
    const LOOK = LOOK0.clone(), tLook = LOOK0.clone();
    let R_MIN = 18, R_MAX = 120, zoomed = false, sized = false;
    let lastInput = -1e9, idle = 0;
    const clampLook = v => { v.x = Math.min(W / 2, Math.max(-W / 2, v.x)); v.z = Math.min(D / 2 + 2, Math.max(-D / 2 - 2, v.z)); v.y = LOOK0.y; return v; };
    const clampPhi = p => Math.min(PHI_MAX, Math.max(PHI_MIN, p));
    const placeCamera = () => {
        const sp = Math.sin(rig.phi);
        camera.position.set(LOOK.x + rig.r * sp * Math.sin(rig.theta), LOOK.y + rig.r * Math.cos(rig.phi), LOOK.z + rig.r * sp * Math.cos(rig.theta));
        camera.lookAt(LOOK);
    };

    // цикл: кадр нужен, когда что-то поменялось; на скрытой вкладке и вне экрана rAF останавливается полностью
    let needsRender = true, loopOn = false, onScreen = true, prev = performance.now();
    const frame = now => {
        if (document.hidden || !onScreen) { loopOn = false; return; }
        if (!canvas.clientWidth) { requestAnimationFrame(frame); return; }
        const dt = Math.min(50, now - prev) / 16.67; prev = now;

        if (!drag) {
            rig.tTheta += rig.vTheta * dt; rig.tPhi = clampPhi(rig.tPhi + rig.vPhi * dt);
            const damp = Math.pow(.92, dt); rig.vTheta *= damp; rig.vPhi *= damp;
            const since = now - lastInput;
            const want = reduced || hot >= 0 ? 0 : since > 3500 ? Math.min(1, (since - 3500) / 3000) : 0;
            idle += (want - idle) * .02 * dt;
            rig.tTheta += .0012 * idle * dt;
        }
        // зазор до цели: если камера ещё едет, кадр нужен
        const gap = Math.abs(rig.tTheta - rig.theta) + Math.abs(rig.tPhi - rig.phi) + Math.abs(rig.tR - rig.r) + LOOK.distanceTo(tLook);
        const a = 1 - Math.pow(1 - .16, dt), zg = 1 - Math.pow(1 - .14, dt);
        rig.theta += (rig.tTheta - rig.theta) * a;
        rig.phi += (rig.tPhi - rig.phi) * a;
        rig.r += (rig.tR - rig.r) * zg; LOOK.lerp(tLook, zg);

        // рост домов волной слева направо при загрузке и при смене данных
        intro = Math.min(1, intro + .012 * dt);
        let growing = false;
        for (let k = 0; k < N; k++) {
            const col = Math.floor(k / 7), gate = Math.min(1, Math.max(0, intro * 1.6 - col / W * .6));
            const target = height(vals[k]) * (reduced ? 1 : gate);
            const nv = shown[k] + (target - shown[k]) * (1 - Math.pow(1 - .14, dt));
            if (Math.abs(nv - shown[k]) > 1e-4) growing = true;
            shown[k] = nv;
        }
        // ничего не меняется — GPU не трогаем, цикл стоит до следующего ввода или автовращения
        if (needsRender || colorsDirty || growing || gap > 1e-4) {
            needsRender = false;
            placeCamera();
            if (growing || colorsDirty) updateBars();
            renderer.render(scene, camera);
            requestAnimationFrame(frame);
        } else {
            loopOn = false;
            if (!reduced) setTimeout(kick, 3600);   // автовращение начнётся через 3,5 с без ввода
        }
    };
    const kick = () => {
        needsRender = true;
        if (!loopOn && !document.hidden && onScreen) { loopOn = true; prev = performance.now(); requestAnimationFrame(frame); }
    };

    const m4 = new THREE.Matrix4(), q = new THREE.Quaternion(), pV = new THREE.Vector3(), sV = new THREE.Vector3();
    const updateBars = () => {
        for (let k = 0; k < N; k++) {
            const [x, z] = cellPos(k), h = Math.max(.02, shown[k]);
            pV.set(x, 0, z); sV.set(.84, h, .84); m4.compose(pV, q, sV); bars.setMatrixAt(k, m4);
            bars.setColorAt(k, k === hot ? HOT : LV[lvl(vals[k])]);
        }
        bars.instanceMatrix.needsUpdate = true; bars.instanceColor.needsUpdate = true;
        colorsDirty = false;
    };

    const resize = () => {
        const r = canvas.getBoundingClientRect(); if (!r.width) return;
        renderer.setSize(r.width, r.height, false);
        camera.aspect = r.width / r.height; camera.updateProjectionMatrix();
        // дистанция «домой»: весь год помещается в ширину канваса при любом размере окна
        const hf = 2 * Math.atan(Math.tan(THREE.MathUtils.degToRad(camera.fov / 2)) * camera.aspect);
        HOME.r = Math.max((W / 2 + 1) / Math.tan(hf / 2), 11 / Math.tan(THREE.MathUtils.degToRad(camera.fov / 2)));
        R_MAX = HOME.r * 1.5; R_MIN = Math.min(18, HOME.r * .3);
        if (!zoomed) { rig.tR = HOME.r; if (!sized) rig.r = HOME.r; }
        sized = true;
        kick();
    };
    new ResizeObserver(resize).observe(canvas);
    new IntersectionObserver(([e]) => { onScreen = e.isIntersecting; if (onScreen) kick(); }).observe(canvas);
    document.addEventListener('visibilitychange', () => { if (!document.hidden) kick(); });

    /* ввод: тяга с pointer lock и курсором, переходящим на противоположный край; колесо — зум к курсору; двойной клик — домой */
    const fake = document.getElementById('fakecur');
    const ndc = new THREE.Vector2();
    let drag = null, fx = 0, fy = 0;
    canvas.addEventListener('pointerdown', e => {
        if (e.button !== 0 && e.button !== 2) return;
        lastInput = performance.now();
        drag = { x: e.clientX, y: e.clientY, mouse: e.pointerType === 'mouse', locked: false, samples: [], pan: e.button === 2 || e.shiftKey };
        rig.vTheta = rig.vPhi = 0; fx = e.clientX; fy = e.clientY;
        canvas.classList.add('dragging'); onHover(-1);
        if (!drag.mouse) canvas.setPointerCapture(e.pointerId);
        else { try { const p = canvas.requestPointerLock({ unadjustedMovement: true }); p && p.catch && p.catch(() => { try { canvas.requestPointerLock(); } catch (_) {} }); } catch (_) { try { canvas.requestPointerLock(); } catch (_) {} } }
        kick();
    });
    document.addEventListener('pointerlockchange', () => {
        const locked = document.pointerLockElement === canvas;
        if (drag) drag.locked = locked;
        if (drag && !locked && drag.mouse) endDrag();   // lock снят (Esc) — жест кончился
        if (fake) { fake.hidden = !locked; if (locked) fake.style.transform = `translate(${fx}px, ${fy}px)`; }
    });
    document.addEventListener('pointermove', e => {
        if (!drag) return;
        let dx, dy;
        if (drag.locked) { dx = e.movementX; dy = e.movementY; }
        else { dx = e.clientX - drag.x; dy = e.clientY - drag.y; drag.x = e.clientX; drag.y = e.clientY; }
        if (drag.pan) {
            // сдвиг по земле; масштаб такой, что город едет за курсором на любом расстоянии
            const k = rig.r * .0016, c = Math.cos(rig.theta), s = Math.sin(rig.theta);
            tLook.x -= (dx * c + dy * s) * k; tLook.z -= (-dx * s + dy * c) * k; clampLook(tLook);
        } else { rig.tTheta -= dx * SENS; rig.tPhi = clampPhi(rig.tPhi - dy * SENS); }
        const now = performance.now();
        drag.samples.push([now, dx, dy]); while (drag.samples.length && now - drag.samples[0][0] > 90) drag.samples.shift();
        lastInput = now;
        if (drag.locked && fake) {
            const vw = innerWidth, vh = innerHeight;
            fx = ((fx + dx) % vw + vw) % vw; fy = ((fy + dy) % vh + vh) % vh;
            fake.style.transform = `translate(${fx}px, ${fy}px)`;
        }
        kick();
    });
    const endDrag = () => {
        if (!drag) return;
        // бросок: средняя скорость за последние ~90 мс
        const s = drag.samples, now = performance.now();
        if (!drag.pan && s.length && now - s[s.length - 1][0] < 60) {
            const dt = Math.max(16, now - s[0][0]);
            const sx = s.reduce((a, p) => a + p[1], 0), sy = s.reduce((a, p) => a + p[2], 0);
            rig.vTheta = -sx * SENS / dt * 16.67; rig.vPhi = -sy * SENS / dt * 16.67;
        }
        drag = null; canvas.classList.remove('dragging');
        if (document.pointerLockElement === canvas) document.exitPointerLock();
        if (fake) fake.hidden = true;
        kick();
    };
    document.addEventListener('pointerup', endDrag);
    document.addEventListener('pointercancel', endDrag);
    const ground = new THREE.Plane(new THREE.Vector3(0, 1, 0), -LOOK0.y), hitP = new THREE.Vector3();
    canvas.addEventListener('wheel', e => {
        e.preventDefault();
        lastInput = performance.now();
        // строки и страницы колеса → пиксели; щипок трекпада приходит как ctrl+wheel и мельче
        let dy = e.deltaY * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1);
        dy = Math.max(-120, Math.min(120, dy)) * (e.ctrlKey ? 2.2 : 1);
        const nr = Math.min(R_MAX, Math.max(R_MIN, rig.tR * Math.exp(dy * .0016)));
        const f = nr / rig.tR; if (f === 1) return;
        // зум к точке под курсором: она остаётся на месте, а цель съезжает к ней
        const r = canvas.getBoundingClientRect();
        ndc.set((e.clientX - r.left) / r.width * 2 - 1, -((e.clientY - r.top) / r.height) * 2 + 1);
        raycaster.setFromCamera(ndc, camera);
        if (raycaster.ray.intersectPlane(ground, hitP)) tLook.sub(hitP).multiplyScalar(f).add(hitP);
        // отдаление за домашнюю дистанцию возвращает к центру города
        if (f > 1) tLook.lerp(LOOK0, Math.min(1, Math.max(0, (nr - HOME.r * .8) / (R_MAX - HOME.r * .8))));
        clampLook(tLook);
        rig.tR = nr; zoomed = true;
        kick();
    }, { passive: false });
    canvas.addEventListener('contextmenu', e => e.preventDefault());
    canvas.addEventListener('dblclick', () => {
        lastInput = performance.now(); rig.vTheta = rig.vPhi = 0;
        rig.tTheta = HOME.theta + Math.round((rig.theta - HOME.theta) / (2 * Math.PI)) * 2 * Math.PI; rig.tPhi = HOME.phi; rig.tR = HOME.r; tLook.copy(LOOK0); zoomed = false;
        kick();
    });

    // наведение: луч по InstancedMesh → instanceId → день
    const setHot = k => { if (k === hot) return; hot = k; colorsDirty = true; kick(); };
    canvas.addEventListener('mousemove', e => {
        if (drag) return;
        const r = canvas.getBoundingClientRect(), x = e.clientX - r.left, y = e.clientY - r.top;
        ndc.set(x / r.width * 2 - 1, -(y / r.height) * 2 + 1);
        raycaster.setFromCamera(ndc, camera);
        const hit = raycaster.intersectObject(bars, false)[0];
        const k = hit ? hit.instanceId : -1;
        setHot(k);
        onHover(k, x, y);
    });
    canvas.addEventListener('mouseleave', () => { if (!drag) { setHot(-1); onHover(-1); } });

    // данные: pool — посты пула по дням, published — опубликовано по дням; интро заново: дома растут волной
    const setData = (poolDays, publishedDays) => {
        pool = poolDays; published = publishedDays;
        N = Math.min(CAP, pool.length); bars.count = N;
        vals = src === 'pub' ? published : pool;
        intro = 0; colorsDirty = true; kick();
    };
    const setSource = s => {
        src = s; vals = s === 'pub' ? published : pool;
        intro = 0; colorsDirty = true; kick();
    };

    resize();
    kick();
    return { setData, setSource, highlight: k => setHot(k) };
}
