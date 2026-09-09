import { Modal, Form, Input, InputNumber, Select, Upload } from 'antd'
import { InboxOutlined, SendOutlined } from '@ant-design/icons'
import { CATEGORIES, CAMPUSES, CONDITIONS } from '../data/products'
import { ripple } from '../utils/ripple'

const dark = {
  background: '#0d0d14',
  border: '1px solid rgba(255,255,255,.1)',
  borderRadius: 20,
}

const fieldStyle = {
  background: 'rgba(255,255,255,.05)',
  border: '1px solid rgba(255,255,255,.1)',
  color: '#fafafa',
}

export default function PublishModal({ open, onClose, onSubmit }) {
  const [form] = Form.useForm()

  const handleFinish = (values) => {
    onSubmit(values)
    form.resetFields()
  }

  return (
    <Modal
      open={open}
      onCancel={onClose}
      footer={null}
      width={560}
      styles={{
        content: dark,
        header: { background: '#0d0d14', borderBottom: '1px solid rgba(255,255,255,.06)', borderRadius: '20px 20px 0 0' },
        mask: { background: 'rgba(5,5,10,.7)', backdropFilter: 'blur(6px)' },
      }}
      title={<span className="font-display text-lg text-white">发布闲置 <span className="text-gradient">✦</span></span>}
    >
      <Form form={form} layout="vertical" onFinish={handleFinish} requiredMark={false} className="mt-2">
        <Form.Item name="title" label={<span className="text-zinc-400">物品标题</span>} rules={[{ required: true, message: '给宝贝起个响亮的标题吧' }]}>
          <Input placeholder="例如：iPhone 13 星光色 128G 国行" style={{ ...fieldStyle, height: 42, borderRadius: 12 }} />
        </Form.Item>

        <div className="grid grid-cols-2 gap-4">
          <Form.Item name="price" label={<span className="text-zinc-400">出手价（元）</span>} rules={[{ required: true, message: '填个价格呗' }]}>
            <InputNumber min={0} placeholder="0" prefix="¥" style={{ ...fieldStyle, width: '100%', height: 42, borderRadius: 12 }} />
          </Form.Item>
          <Form.Item name="category" label={<span className="text-zinc-400">分类</span>} rules={[{ required: true, message: '选个分类' }]}>
            <Select
              placeholder="选择分类"
              options={CATEGORIES.filter((c) => c !== '全部').map((c) => ({ value: c, label: c }))}
              variant="filled"
              styles={{ popup: { root: { background: '#14141d' } } }}
            />
          </Form.Item>
          <Form.Item name="campus" label={<span className="text-zinc-400">校区</span>} rules={[{ required: true, message: '选个校区' }]}>
            <Select
              placeholder="选择校区"
              options={CAMPUSES.map((c) => ({ value: c, label: c }))}
              variant="filled"
              styles={{ popup: { root: { background: '#14141d' } } }}
            />
          </Form.Item>
          <Form.Item name="condition" label={<span className="text-zinc-400">成色</span>} rules={[{ required: true, message: '选个成色' }]}>
            <Select
              placeholder="选择成色"
              options={CONDITIONS.map((c) => ({ value: c, label: c }))}
              variant="filled"
              styles={{ popup: { root: { background: '#14141d' } } }}
            />
          </Form.Item>
        </div>

        <Form.Item name="desc" label={<span className="text-zinc-400">宝贝描述</span>}>
          <Input.TextArea
            rows={3}
            maxLength={200}
            showCount
            placeholder="使用情况、瑕疵、入手渠道、为什么出……写清楚更好卖"
            style={{ ...fieldStyle, borderRadius: 12 }}
          />
        </Form.Item>

        <Form.Item name="image" label={<span className="text-zinc-400">实物照片</span>} valuePropName="fileList" getValueFromEvent={(e) => e?.fileList}>
          <Upload.Dragger
            beforeUpload={() => false}
            maxCount={1}
            accept="image/*"
            style={{ background: 'rgba(255,255,255,.04)', border: '1px dashed rgba(255,255,255,.2)', borderRadius: 12 }}
          >
            <p className="text-3xl mb-1">
              <InboxOutlined className="text-gradient" />
            </p>
            <p className="text-zinc-400 text-sm">点击或拖拽上传照片</p>
            <p className="text-zinc-600 text-xs">真实照片更容易成交哦</p>
          </Upload.Dragger>
        </Form.Item>

        <button type="submit" className="btn-primary ripple w-full !h-12 text-base mt-2" onClick={ripple}>
          <SendOutlined /> 立即上架
        </button>
      </Form>
    </Modal>
  )
}