const img = (seed) => `https://picsum.photos/seed/${seed}/640/480`

export const CATEGORIES = ['全部', '数码电子', '教材书籍', '生活日用', '服饰鞋包', '运动户外', '美妆个护']

export const CATEGORY_COLOR = {
  数码电子: '#5b7cff',
  教材书籍: '#4d9fff',
  生活日用: '#8b93a8',
  服饰鞋包: '#a0a8ff',
  运动户外: '#5fd0b0',
  美妆个护: '#c9a0ff',
}

export const CAMPUSES = ['东区', '西区', '南湖校区', '未来城']
export const CONDITIONS = ['全新', '九九新', '九成新', '八成新', '七成新']

export const SELLER_REPLIES = [
  '在的在的，随时可以看实物～',
  '价格好商量，诚心要的话给你抹个零头',
  '东西保养得很好，没有暗病',
  '可以约在东区食堂门口当面交易',
  '好的好的，那我给你留着！',
]

export const INITIAL_PRODUCTS = [
  {
    id: 1, title: 'iPhone 13 星光色 128G 国行', price: 2899, oldPrice: 5999,
    category: '数码电子', campus: '东区', condition: '九成新',
    desc: '自用一手机，全程贴膜戴壳，电池健康 87%。无拆无修无暗病，原装盒子发票都在，支持当面验机。送三个手机壳和一根原装数据线。',
    img: img('iphone13'), views: 2381, likes: 126, time: '10 分钟前',
    tags: ['可验机', '原装无修', '箱说全'],
    seller: { name: '陈学长', avatar: 'https://i.pravatar.cc/100?img=12', online: true, deals: 23, rating: 4.9 },
  },
  {
    id: 2, title: '考研数学一全套复习资料（李永乐+张宇）', price: 45, oldPrice: 168,
    category: '教材书籍', campus: '西区', condition: '八成新',
    desc: '25 考研自用资料，李永乐复习全书、张宇 1000 题、历年真题详解，笔记工整，重点全部标好。数学一 138 分上岸，沾沾喜气。',
    img: img('mathbook'), views: 899, likes: 64, time: '32 分钟前',
    tags: ['上岸笔记', '划重点'],
    seller: { name: '苏学姐', avatar: 'https://i.pravatar.cc/100?img=47', online: true, deals: 41, rating: 5.0 },
  },
  {
    id: 3, title: '佳能 AE-1 复古胶片相机 + 50mm f1.8', price: 680, oldPrice: 1200,
    category: '数码电子', campus: '南湖校区', condition: '九成新',
    desc: '豆瓣神机，机械快门声非常治愈。功能全好，测光准确，镜头无霉无雾。附赠两卷柯达金 200，上手即拍。',
    img: img('filmcamera'), views: 1520, likes: 203, time: '1 小时前',
    tags: ['豆瓣神机', '送胶卷'],
    seller: { name: '阿澈', avatar: 'https://i.pravatar.cc/100?img=33', online: false, deals: 8, rating: 4.8 },
  },
  {
    id: 4, title: '宿舍小冰箱 62L 一级能效', price: 220, oldPrice: 499,
    category: '生活日用', campus: '东区', condition: '九成新',
    desc: '用了一年，制冷效果嘎嘎好，声音特别小不会吵到室友。因换宿舍出，可小刀，需自提。',
    img: img('minifridge'), views: 2103, likes: 98, time: '2 小时前',
    tags: ['可小刀', '需自提'],
    seller: { name: '王大力', avatar: 'https://i.pravatar.cc/100?img=51', online: true, deals: 15, rating: 4.7 },
  },
  {
    id: 5, title: 'Air Jordan 1 高帮 黑白熊猫 42 码', price: 520, oldPrice: 1299,
    category: '服饰鞋包', campus: '未来城', condition: '全新',
    desc: '官网抽签中的，尺码不合适含泪出。原盒原吊牌，支持得物鉴定。全新未下地，可当面看鞋。',
    img: img('aj1'), views: 1876, likes: 175, time: '3 小时前',
    tags: ['全新未拆', '支持鉴定'],
    seller: { name: '鞋狗本狗', avatar: 'https://i.pravatar.cc/100?img=15', online: true, deals: 31, rating: 4.9 },
  },
  {
    id: 6, title: '尤尼克斯羽毛球拍 天斧 99 Pro', price: 260, oldPrice: 680,
    category: '运动户外', campus: '西区', condition: '八成新',
    desc: '校队训练拍，拉的是 BG80 线 26 磅。拍框有轻微使用痕迹，无磕碰无修复。送拍套和手胶。',
    img: img('badminton'), views: 743, likes: 52, time: '4 小时前',
    tags: ['送手胶', '校队同款'],
    seller: { name: '林丹分丹', avatar: 'https://i.pravatar.cc/100?img=60', online: false, deals: 12, rating: 4.8 },
  },
  {
    id: 7, title: '兰蔻小黑瓶精华 30ml 全新未拆封', price: 399, oldPrice: 760,
    category: '美妆个护', campus: '南湖校区', condition: '全新',
    desc: '双十一囤多了，出一瓶。专柜购入，有购买记录，塑封完整，保质期到 2027 年。',
    img: img('lancome'), views: 1120, likes: 88, time: '5 小时前',
    tags: ['有购买记录', '日期新鲜'],
    seller: { name: '桃桃乌龙', avatar: 'https://i.pravatar.cc/100?img=45', online: true, deals: 19, rating: 5.0 },
  },
  {
    id: 8, title: '雷蛇黑寡妇机械键盘 绿轴', price: 150, oldPrice: 399,
    category: '数码电子', campus: '东区', condition: '八成新',
    desc: '宿舍码字神器，绿轴手感清脆。按键全触发，灯光全亮，已全面清洁消毒。换键盘故出。',
    img: img('keyboard'), views: 654, likes: 37, time: '6 小时前',
    tags: ['已消毒', '手感好'],
    seller: { name: '码农预备役', avatar: 'https://i.pravatar.cc/100?img=20', online: true, deals: 6, rating: 4.6 },
  },
  {
    id: 9, title: '雅思真题 4-19 + 王陆语料库 全套', price: 29, oldPrice: 129,
    category: '教材书籍', campus: '未来城', condition: '七成新',
    desc: '雅思 7.5 上岸出资料。剑桥真题 4-19 全套 + 王陆听力语料库，部分有铅笔笔记可擦。送网盘电子资料。',
    img: img('ielts'), views: 567, likes: 43, time: '8 小时前',
    tags: ['送电子资料', '上岸学姐'],
    seller: { name: '苏学姐', avatar: 'https://i.pravatar.cc/100?img=47', online: true, deals: 41, rating: 5.0 },
  },
  {
    id: 10, title: '美利达公爵 300 山地车 27 速', price: 750, oldPrice: 1898,
    category: '运动户外', campus: '东区', condition: '八成新',
    desc: '校内通勤利器，变速顺滑刹车灵敏。前叉可锁死，适合上下课代步。车架 17 寸，送车锁和尾灯。',
    img: img('mtb'), views: 1345, likes: 91, time: '10 小时前',
    tags: ['送车锁', '代步神器'],
    seller: { name: '追风少年', avatar: 'https://i.pravatar.cc/100?img=8', online: false, deals: 4, rating: 4.7 },
  },
  {
    id: 11, title: '北欧风落地灯 三色变光', price: 66, oldPrice: 159,
    category: '生活日用', campus: '西区', condition: '九成新',
    desc: '氛围感拉满的三色变光落地灯，暖光适合宿舍拍照。因毕业出，灯罩无泛黄，底座稳。',
    img: img('lamp'), views: 432, likes: 29, time: '12 小时前',
    tags: ['氛围感', '毕业出'],
    seller: { name: '灯灯等灯', avatar: 'https://i.pravatar.cc/100?img=25', online: true, deals: 9, rating: 4.8 },
  },
  {
    id: 12, title: 'The North Face 700 蓬羽绒服 M 码', price: 430, oldPrice: 1498,
    category: '服饰鞋包', campus: '南湖校区', condition: '九成新',
    desc: '1996 经典款，正品可验。去年冬天穿过几次，已干洗。充绒量足，南方冬天一件过冬。',
    img: img('tnf'), views: 987, likes: 76, time: '1 天前',
    tags: ['已干洗', '正品可验'],
    seller: { name: '怕冷星人', avatar: 'https://i.pravatar.cc/100?img=38', online: true, deals: 11, rating: 4.9 },
  },
]