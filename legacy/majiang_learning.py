import random

# 公共变量区：
seats = ["east","south","west","north"]

def create_title(suit: str):
    mojang = []
    for i in range(1, 5):
        for j in range(1, 10):
            tile = f"{j}{suit}"
            mojang.append(tile)
    return mojang

def creat_honor_tiles():
    honor_tiles = []
    caselist = ["E", "S", "W", "N", "P", "F", "C"]
    for i in range(1, 5):
        for x in caselist:
            honor_tiles.append(x)
    return honor_tiles

def creat_deck():
    alldeck = []
    alldeck.extend(create_title("m"))
    alldeck.extend(create_title("p"))
    alldeck.extend(create_title("s"))
    alldeck.extend(creat_honor_tiles())
    return alldeck


def shuffle_wall(wall: list):
    random.shuffle(wall)
    return wall


def draw_tile(wall: list):
    if not wall:
        return None
    tile = wall.pop()
    return tile


def deal_initial_hands(wall: list):

    if len(wall) < 53:
        raise ValueError("wall not enought ,can't complete setup tasks")
    
    hands = {}
    for seat in seats:
        hands[seat] = []
    for _ in range(13):
        for seat in seats:
            tile = draw_tile(wall)
            hands[seat].append(tile)
    hands["east"].append(draw_tile(wall))
    return hands


def discard_tile(hand: list, tile: str):
    if tile in hand:
        hand.remove(tile)
        return tile
    return None

def create_discards():
    discards = {
        seat:[] for seat in seats
    }
    return discards

def discard_to_river(
    hands: dict,
    discards: dict,
    seat: str,
    tile: str
):
    # 1. 找到 seat 的手牌
    # 2. 调用 discard_tile()
    # 3. 如果成功，添加到对应牌河
    # 4. 返回打牌结果
    player_hand = hands[seat]
    discarded = discard_tile(player_hand,tile)
    if discarded :
        discards[seat].append(discarded)
    return discarded

def create_game():
    wall = creat_deck()
    wall = shuffle_wall(wall)
    hands = deal_initial_hands(wall)
    discards = create_discards()

    game = {
        "wall":wall,
        "hands":hands,
        "discards":discards,
        "current_player":"east",
        "turn":0,
    }
    return game

