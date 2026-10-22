#!/usr/bin/env python3
"""Отложенные посты Telegram от имени вашего аккаунта (MTProto, библиотека Telethon из tools/tg-lib).

У ботов в Telegram нет отложенной отправки, а у аккаунта есть: пост ставится в «Отложенные» самого канала и уходит в
назначенное время, даже если этот компьютер выключен. Вызывается из PHP (lib/telegram_user.php): JSON-запрос в stdin,
JSON-ответ в stdout. Секреты (api_hash, сессия) приходят в запросе и нигде не сохраняются.

Действия: send_code, resend_code, sign_in, password, check_channel, schedule, cancel,
search_channels (поиск каналов по запросам — для автоподбора источников), similar_channels (похожие каналы),
read_chats (новые сообщения чатов, без вступления), search_chats (поиск публичных групп — для радара лидов).
Режим `tg_user.py qr-login <файл запроса>` — вход по QR-коду (когда код входа не доходит): работает до двух минут в фоне и пишет
состояние в файл из запроса (state_file), см. lib/telegram_user.php.
"""
import asyncio
import datetime
import json
import os
import sys
import time

sys.dont_write_bytecode = True
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), 'tg-lib'))

from telethon import TelegramClient, errors, functions, types  # noqa: E402
from telethon.sessions import StringSession  # noqa: E402

ERRORS = {
    errors.ApiIdInvalidError: 'api_id или api_hash не подходят: проверьте их на my.telegram.org.',
    errors.PhoneNumberInvalidError: 'Номер телефона указан неверно: нужен формат +79991234567.',
    errors.PhoneNumberBannedError: 'Этот номер заблокирован в Telegram.',
    errors.PhoneCodeInvalidError: 'Код неверный.',
    errors.PhoneCodeExpiredError: 'Код устарел: запросите новый.',
    errors.PasswordHashInvalidError: 'Пароль двухэтапной защиты неверный.',
    errors.SessionPasswordNeededError: 'Нужен пароль двухэтапной защиты.',
    errors.AuthKeyUnregisteredError: 'Вход устарел: подключите аккаунт заново.',
    errors.SessionRevokedError: 'Сессия завершена в Telegram: подключите аккаунт заново.',
    errors.UserDeactivatedError: 'Аккаунт удалён или заблокирован.',
}


def describe(error):
    for kind, text in ERRORS.items():
        if isinstance(error, kind):
            return text
    if isinstance(error, errors.FloodWaitError):
        return 'Telegram просит подождать %d с и повторить.' % error.seconds
    if isinstance(error, errors.ScheduledTooMuchError):
        return 'В канале уже слишком много отложенных постов (лимит Telegram — 100).'
    if isinstance(error, errors.ChatAdminRequiredError):
        return 'У вашего аккаунта нет права публиковать в этом канале.'
    return '%s: %s' % (type(error).__name__, error)


async def find_channel(client, ref):
    """Канал по @имени или числовому id; для id сначала подгружаются диалоги (иначе у сессии нет access_hash)."""
    ref = str(ref).strip()
    if ref.startswith('@') or not ref.lstrip('-').isdigit():
        return await client.get_entity(ref)
    try:
        return await client.get_entity(int(ref))
    except ValueError:
        await client.get_dialogs()
        return await client.get_entity(int(ref))


def code_info(sent):
    """Куда Telegram отправил код (app — в чат «Telegram» в приложении, sms, call…) и что можно запросить дальше."""
    def name(kind):
        return type(kind).__name__.replace('SentCodeType', '').lower() if kind else ''
    return {'phone_code_hash': sent.phone_code_hash, 'code_type': name(sent.type), 'next_type': name(sent.next_type)}


def channel_info(chat, origin):
    """Публичный канал (не группа) с @именем или None."""
    if not isinstance(chat, types.Channel) or not getattr(chat, 'broadcast', False) or not chat.username:
        return None
    return {'username': chat.username, 'title': chat.title or '', 'subscribers': chat.participants_count or 0, 'origin': origin}


def group_info(chat, origin):
    """Публичная группа-чат (мегагруппа) с @именем или None: каналы-витрины, приватные группы и обычные чаты пропускаются."""
    if not isinstance(chat, types.Channel) or not chat.username:
        return None
    if getattr(chat, 'broadcast', False) or not (getattr(chat, 'megagroup', False) or getattr(chat, 'gigagroup', False)):
        return None
    return {'username': chat.username, 'title': chat.title or '', 'members': chat.participants_count or 0, 'query': origin}


async def read_chats(client, chats):
    """Читать новые сообщения из списка чатов. Возвращает {'chats': [...], 'ok': true}."""
    result_chats = []

    for chat_spec in chats:
        ref = chat_spec.get('ref', '')
        min_id = int(chat_spec.get('min_id', 0))
        limit = int(chat_spec.get('limit', 100))
        if min_id == 0:
            limit = min(limit, 30)  # первый запуск — только последние 30

        result_chat = {
            'ref': ref,
            'chat_id': None,
            'title': '',
            'username': '',
            'error': None,
            'messages': []
        }

        try:
            entity = await find_channel(client, ref)

            # Проверяем что это чат/группа, не канал
            if isinstance(entity, types.Channel) and getattr(entity, 'broadcast', False):
                result_chat['error'] = 'Это канал, а не чат: людей там не видно'
                result_chats.append(result_chat)
                continue

            result_chat['chat_id'] = entity.id
            result_chat['title'] = getattr(entity, 'title', '')
            result_chat['username'] = getattr(entity, 'username', '') or ''

            # Читаем сообщения
            async for message in client.iter_messages(entity, min_id=min_id, limit=limit):
                # Пропускаем служебные, свои, от ботов, без текста
                if (message.action or message.out or getattr(message, 'from_id', None) is None or
                    getattr(message.sender, 'is_bot', False) or not message.text):
                    continue

                sender_id = getattr(message.from_id, 'user_id', 0) if hasattr(message.from_id, 'user_id') else 0
                sender = getattr(message, 'sender', None)
                sender_name = ''
                sender_username = ''

                if sender:
                    first = getattr(sender, 'first_name', '')
                    last = getattr(sender, 'last_name', '')
                    sender_name = (first + (' ' + last if last else '')).strip()[:64]
                    sender_username = getattr(sender, 'username', '') or ''

                result_chat['messages'].append({
                    'id': message.id,
                    'date': int(message.date.timestamp()) if message.date else int(time.time()),
                    'text': message.text,
                    'sender_id': sender_id,
                    'sender_username': sender_username,
                    'sender_name': sender_name,
                    'is_bot': getattr(message.sender, 'is_bot', False),
                    'out': message.out
                })

        except errors.FloodWaitError as e:
            if e.seconds > 60:
                # Слишком долгий ввод — прерываем весь запрос
                break
            result_chat['error'] = 'Telegram просит подождать %d с' % e.seconds
        except (errors.ChannelPrivateError, errors.ChatAdminRequiredError):
            result_chat['error'] = 'Закрыт для чтения без вступления'   # в чат не вступаем
        except Exception as e:
            result_chat['error'] = describe(e)

        result_chats.append(result_chat)

    return {'chats': result_chats, 'ok': True}


async def search_channels(client, queries, limit):
    """Глобальный поиск Telegram по каждому запросу: реально существующие каналы в порядке, в каком их ранжирует Telegram."""
    found = {}
    for query in queries[:12]:
        query = str(query).strip()
        if len(query) < 2:
            continue
        try:
            result = await client(functions.contacts.SearchRequest(q=query, limit=min(limit, 100)))
        except errors.FloodWaitError:
            break   # лимит поиска исчерпан: возвращаем, что успели найти
        for chat in result.chats:
            info = channel_info(chat, query)
            if info and info['username'].lower() not in found:
                found[info['username'].lower()] = info
    return list(found.values())


async def search_chats(client, queries, limit):
    """Глобальный поиск Telegram: публичные группы (не каналы) по каждому запросу, без вступления."""
    found = {}
    for query in queries[:15]:
        query = str(query).strip()
        if len(query) < 2:
            continue
        try:
            result = await client(functions.contacts.SearchRequest(q=query, limit=min(limit, 100)))
        except errors.FloodWaitError:
            break   # лимит поиска исчерпан: возвращаем, что успели найти
        for chat in result.chats:
            info = group_info(chat, query)
            if info and info['username'].lower() not in found:
                found[info['username'].lower()] = info
    return list(found.values())


async def similar_channels(client, refs):
    """«Похожие каналы» из рекомендаций Telegram для каждого канала из списка."""
    found = {}
    for ref in refs[:8]:
        try:
            entity = await client.get_entity('@' + str(ref).lstrip('@'))
            result = await client(functions.channels.GetChannelRecommendationsRequest(channel=entity))
        except errors.FloodWaitError:
            break
        except Exception:  # noqa: BLE001 — канал исчез или рекомендаций нет: просто следующий
            continue
        for chat in result.chats:
            info = channel_info(chat, str(ref).lstrip('@'))
            if info and info['username'].lower() not in found:
                found[info['username'].lower()] = info
    return list(found.values())


def upload_progress(path):
    """Колбэк заливки для send_file: пишет в файл прогресса тот же JSON, что telegram_progress в PHP, не чаще раза в 0,3 с."""
    last = [0.0]

    def callback(current, total):
        now = time.monotonic()
        if not total or (now - last[0] < 0.3 and current < total):
            return
        last[0] = now
        try:
            with open(path + '.tmp', 'w', encoding='utf-8') as handle:
                json.dump({'stage': 'Отправка в Telegram', 'percent': round(current / total * 100), 'at': int(time.time())}, handle, ensure_ascii=False)
            os.replace(path + '.tmp', path)   # PHP не прочтёт недописанный файл
        except OSError:
            pass   # прогресс не главное: отправка идёт дальше
    return callback


def video_attributes(item):
    return [types.DocumentAttributeVideo(
        duration=int(item.get('duration') or 0), w=int(item.get('width') or 0), h=int(item.get('height') or 0),
        supports_streaming=True)]


async def run(request, client):
    action = request['action']
    if action == 'send_code':
        return code_info(await client.send_code_request(request['phone']))
    if action == 'resend_code':
        # следующий способ доставки, который предлагает Telegram (обычно SMS после кода в приложении)
        return code_info(await client(functions.auth.ResendCodeRequest(request['phone'], request['phone_code_hash'])))
    if action == 'read_chats':
        return await read_chats(client, request.get('chats') or [])
    if action == 'sign_in':
        try:
            await client.sign_in(request['phone'], request.get('code', ''), phone_code_hash=request.get('phone_code_hash'))
        except errors.SessionPasswordNeededError:
            if not request.get('password'):
                return {'needs_password': True}
            await client.sign_in(password=request['password'])
        me = await client.get_me()
        name = ' '.join(filter(None, [me.first_name, me.last_name]))
        return {'user': {'id': me.id, 'name': name, 'username': me.username or ''}}
    if action == 'password':
        await client.sign_in(password=request['password'])
        me = await client.get_me()
        return {'user': {'id': me.id, 'name': ' '.join(filter(None, [me.first_name, me.last_name])), 'username': me.username or ''}}
    if not await client.is_user_authorized():
        raise errors.AuthKeyUnregisteredError(None)
    if action == 'search_channels':
        return {'channels': await search_channels(client, request.get('queries') or [], int(request.get('limit') or 30))}
    if action == 'search_chats':
        return {'chats': await search_chats(client, request.get('queries') or [], int(request.get('limit') or 30))}
    if action == 'similar_channels':
        return {'channels': await similar_channels(client, request.get('channels') or [])}
    entity = await find_channel(client, request['channel'])
    if action == 'check_channel':
        me = await client.get_me()
        account = ' '.join(filter(None, [me.first_name, me.last_name])) + (' @' + me.username if me.username else '')
        # Права берём прямо из канала, каким его видит этот аккаунт (creator / admin_rights), а не из get_permissions
        rights = getattr(entity, 'admin_rights', None)
        if getattr(entity, 'creator', False):
            role = 'владелец'
        elif rights is not None:
            role = 'администратор' if getattr(rights, 'post_messages', False) else 'администратор без права публиковать'
        elif getattr(entity, 'left', False):
            role = 'не участник канала'
        else:
            role = 'обычный участник'
        can_post = role in ('владелец', 'администратор')
        return {'title': getattr(entity, 'title', ''), 'username': getattr(entity, 'username', '') or '', 'can_post': can_post,
                'role': role, 'account': account}
    if action == 'schedule':
        when = datetime.datetime.fromtimestamp(int(request['publish_at']), tz=datetime.timezone.utc)
        text = request.get('text') or ''
        media = request.get('media') or []
        if not media:
            sent = await client.send_message(entity, text, schedule=when, link_preview=False)
            return {'ids': [sent.id]}
        paths = [item['path'] for item in media]
        progress = {'progress_callback': upload_progress(request['progress_file'])} if request.get('progress_file') else {}
        if len(media) == 1:
            single = media[0]
            sent = await client.send_file(entity, single['path'], caption=text, schedule=when, force_document=False,
                                          supports_streaming=True,
                                          attributes=video_attributes(single) if single['kind'] == 'video' else None, **progress)
            return {'ids': [sent.id]}
        sent = await client.send_file(entity, paths, caption=text, schedule=when, force_document=False, supports_streaming=True, **progress)
        return {'ids': [message.id for message in sent]}
    if action == 'cancel':
        await client(functions.messages.DeleteScheduledMessagesRequest(peer=entity, id=[int(i) for i in request['ids']]))
        return {}
    raise ValueError('Неизвестное действие: %s' % action)


QR_LIFETIME_SECONDS = 150


def write_state(path, state):
    temporary = path + '.tmp'
    with open(temporary, 'w', encoding='utf-8') as handle:
        json.dump(state, handle, ensure_ascii=False)
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)


def qr_svg(url):
    import io
    import segno
    buffer = io.BytesIO()
    segno.make(url, error='m').save(buffer, kind='svg', scale=6, border=2, dark='#000', light='#fff', xmldecl=False, svgns=True)
    return buffer.getvalue().decode('utf-8')


def current_run(path, token):
    try:
        with open(path, encoding='utf-8') as handle:
            return json.load(handle).get('token') == token
    except (OSError, ValueError):
        return False


async def qr_login(request_path):
    """Вход по QR: пользователь сканирует код в приложении Telegram (Настройки → Устройства → Подключить устройство)."""
    with open(request_path, encoding='utf-8') as handle:
        request = json.load(handle)
    os.remove(request_path)   # в файле api_hash: сразу удаляем
    path, token = request['state_file'], request['token']
    client = TelegramClient(StringSession(''), int(request['api_id']), request['api_hash'],
                            device_model='Posta', system_version='Linux', app_version='1.0', lang_code='ru', timeout=20)
    state = {'token': token, 'status': 'waiting', 'at': 0}
    try:
        await client.connect()
        qr = await client.qr_login()
        deadline = asyncio.get_event_loop().time() + QR_LIFETIME_SECONDS
        while True:
            if not current_run(path, token) and state['at']:
                return   # запущен новый вход: этот прекращаем
            state.update(status='waiting', url=qr.url, svg=qr_svg(qr.url), at=int(datetime.datetime.now().timestamp()))
            write_state(path, state)
            try:
                await qr.wait(timeout=25)
                break
            except asyncio.TimeoutError:
                if asyncio.get_event_loop().time() > deadline:
                    write_state(path, {'token': token, 'status': 'expired'})
                    return
                await qr.recreate()
            except errors.SessionPasswordNeededError:
                write_state(path, {'token': token, 'status': 'needs_password', 'session': client.session.save()})
                return
        me = await client.get_me()
        write_state(path, {'token': token, 'status': 'ok', 'session': client.session.save(),
                           'user': {'id': me.id, 'name': ' '.join(filter(None, [me.first_name, me.last_name])), 'username': me.username or ''}})
    except Exception as error:  # noqa: BLE001
        write_state(path, {'token': token, 'status': 'error', 'error': describe(error)})
    finally:
        await client.disconnect()


async def main():
    if len(sys.argv) > 2 and sys.argv[1] == 'qr-login':
        await qr_login(sys.argv[2])
        return
    request = json.load(sys.stdin)
    client = TelegramClient(StringSession(request.get('session') or ''), int(request['api_id']), request['api_hash'],
                            device_model='Posta', system_version='Linux', app_version='1.0', lang_code='ru',
                            connection_retries=2, request_retries=2, timeout=20)
    try:
        await client.connect()
        result = await run(request, client)
        result['session'] = client.session.save()
        result['ok'] = True
    except Exception as error:  # noqa: BLE001 — любая ошибка уходит в PHP текстом
        result = {'ok': False, 'error': describe(error)}
    finally:
        await client.disconnect()
    sys.stdout.write(json.dumps(result, ensure_ascii=False))


if __name__ == '__main__':
    asyncio.run(main())
