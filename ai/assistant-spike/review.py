"""One conversation through a member's `hermes serve`, then the connection is kept for HOLD
seconds, as a product service that waits for the instance to go idle would, before it leaves.
For the background review (#114, review.sh). Runs as root with the runtime's Python.

  review.py USER HOLD TEXT [TEXT...]
"""
import asyncio
import sys
import time

sys.path.insert(0, "/spike")
from serve_client import Gateway, short  # noqa: E402


async def main():
    user, hold, texts = sys.argv[1], float(sys.argv[2]), sys.argv[3:]
    async with Gateway(user, timeout=900) as gw:
        await gw.call("client.capabilities", {"server_requests": True})
        sid = await gw.new_session()
        for text in texts:
            result = await gw.turn(sid, text)
            print(f"> {short(text, 90)}\n< {short(result['answer'], 240)}")
            print(f"  {len(result['tools'])} tool calls {result['tools'][:10]}; {result['seconds']} s")
        print(f"last turn done {time.strftime('%H:%M:%S')}; keeping the connection {hold:.0f} s")
        await asyncio.sleep(hold)
    print(f"disconnected {time.strftime('%H:%M:%S')}")


asyncio.run(main())
