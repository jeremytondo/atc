import {
  isStyledText,
  parseColor,
  Renderable,
  type ColorInput,
  type KeyEvent,
  type OptimizedBuffer,
  type RenderableOptions,
  type RenderContext,
  type StyledText,
} from "@opentui/core"

// OpenTUI's SelectRenderable accepts only plain strings. This compatible,
// intentionally small list keeps its keyboard, selection, and scrolling
// behavior while allowing independently styled spans in an option name.

export interface StyledSelectOption {
  readonly name: string | StyledText
  readonly description: string
  readonly descriptionIndent?: number
}

export interface StyledSelectOptions extends RenderableOptions<StyledSelectRenderable> {
  readonly options?: ReadonlyArray<StyledSelectOption>
  readonly selectedIndex?: number
  readonly wrapSelection?: boolean
  readonly selectedBackgroundColor?: ColorInput
  readonly backgroundColor?: ColorInput
  readonly selectedTextColor?: ColorInput
  readonly textColor?: ColorInput
  readonly descriptionColor?: ColorInput
  readonly selectedDescriptionColor?: ColorInput
  readonly showScrollIndicator?: boolean
  readonly showDescription?: boolean
}

export class StyledSelectRenderable extends Renderable {
  protected override _focusable = true
  private items: ReadonlyArray<StyledSelectOption>
  private selectedIndex: number
  private scrollOffset = 0
  private maxVisibleItems = 1
  private readonly wrapSelection: boolean
  private readonly textColor
  private readonly selectedTextColor
  private readonly selectedBackgroundColor
  private readonly backgroundColor
  private readonly descriptionColor
  private readonly selectedDescriptionColor
  private readonly scrollIndicatorColor = parseColor("#666666")
  private readonly showScrollIndicator: boolean

  constructor(context: RenderContext, options: StyledSelectOptions) {
    super(context, { ...options, buffered: true })
    this.items = options.options ?? []
    this.selectedIndex = Math.min(options.selectedIndex ?? 0, Math.max(0, this.items.length - 1))
    this.wrapSelection = options.wrapSelection ?? false
    this.textColor = parseColor(options.textColor ?? "#ffffff")
    this.selectedTextColor = parseColor(options.selectedTextColor ?? "#ffffff")
    this.selectedBackgroundColor = parseColor(options.selectedBackgroundColor ?? "#334455")
    this.backgroundColor = parseColor(options.backgroundColor ?? "transparent")
    this.descriptionColor = parseColor(options.descriptionColor ?? "#888888")
    this.selectedDescriptionColor = parseColor(options.selectedDescriptionColor ?? "#cccccc")
    this.showScrollIndicator = options.showScrollIndicator ?? false
    this.updateScrollOffset()
  }

  get options(): ReadonlyArray<StyledSelectOption> {
    return this.items
  }

  set options(options: ReadonlyArray<StyledSelectOption>) {
    this.items = options
    this.selectedIndex = Math.min(this.selectedIndex, Math.max(0, options.length - 1))
    this.updateScrollOffset()
    this.requestRender()
  }

  getSelectedIndex(): number {
    return this.selectedIndex
  }

  setSelectedIndex(index: number): void {
    if (index < 0 || index >= this.items.length) return
    this.selectedIndex = index
    this.selectionChanged()
  }

  moveUp(steps = 1): void {
    const next = this.selectedIndex - steps
    this.selectedIndex =
      next >= 0 ? next : this.wrapSelection && this.items.length > 0 ? this.items.length - 1 : 0
    this.selectionChanged()
  }

  moveDown(steps = 1): void {
    const next = this.selectedIndex + steps
    this.selectedIndex =
      next < this.items.length
        ? next
        : this.wrapSelection && this.items.length > 0
          ? 0
          : Math.max(0, this.items.length - 1)
    this.selectionChanged()
  }

  selectCurrent(): void {
    const selected = this.items[this.selectedIndex]
    if (selected !== undefined) this.emit("itemSelected", this.selectedIndex, selected)
  }

  override handleKeyPress(key: KeyEvent): boolean {
    if (key.name === "up" || key.name === "k") {
      this.moveUp(key.shift ? 5 : 1)
      return true
    }
    if (key.name === "down" || key.name === "j") {
      this.moveDown(key.shift ? 5 : 1)
      return true
    }
    if (key.name === "return" || key.name === "linefeed") {
      this.selectCurrent()
      return true
    }
    return false
  }

  protected override onResize(_width: number, height: number): void {
    this.maxVisibleItems = Math.max(1, Math.floor(height / 2))
    this.updateScrollOffset()
    this.requestRender()
  }

  protected override renderSelf(_buffer: OptimizedBuffer): void {
    if (!this.visible || this.frameBuffer === null) return
    this.frameBuffer.clear(this.backgroundColor)
    const visible = this.items.slice(this.scrollOffset, this.scrollOffset + this.maxVisibleItems)
    visible.forEach((option, offset) =>
      this.renderOption(option, this.scrollOffset + offset, offset),
    )
    this.renderScrollIndicator()
  }

  private renderOption(option: StyledSelectOption, index: number, visibleIndex: number): void {
    if (this.frameBuffer === null) return
    const selected = index === this.selectedIndex
    const y = visibleIndex * 2
    const nameColor = selected ? this.selectedTextColor : this.textColor
    this.frameBuffer.drawText(" ", 0, y, nameColor)

    if (isStyledText(option.name)) {
      let x = 1
      for (const chunk of option.name.chunks) {
        this.frameBuffer.drawText(
          chunk.text,
          x,
          y,
          chunk.fg ?? nameColor,
          chunk.bg,
          chunk.attributes,
        )
        x += Bun.stringWidth(chunk.text)
      }
    } else {
      this.frameBuffer.drawText(option.name, 1, y, nameColor)
    }

    const descriptionColor = selected ? this.selectedDescriptionColor : this.descriptionColor
    this.frameBuffer.drawText(
      option.description,
      1 + (option.descriptionIndent ?? 0),
      y + 1,
      descriptionColor,
    )
    if (selected) this.frameBuffer.fillRect(0, y, this.width, 2, this.selectedBackgroundColor)
  }

  private renderScrollIndicator(): void {
    if (
      this.frameBuffer === null ||
      !this.showScrollIndicator ||
      this.items.length <= this.maxVisibleItems
    ) {
      return
    }
    const maxOffset = this.items.length - this.maxVisibleItems
    const height = Math.max(1, this.height - 2)
    const y = 1 + Math.floor((this.scrollOffset / maxOffset) * height)
    this.frameBuffer.drawText("█", this.width - 1, y, this.scrollIndicatorColor)
  }

  private selectionChanged(): void {
    this.updateScrollOffset()
    this.requestRender()
    this.emit("selectionChanged", this.selectedIndex, this.items[this.selectedIndex] ?? null)
  }

  private updateScrollOffset(): void {
    const halfVisible = Math.floor(this.maxVisibleItems / 2)
    this.scrollOffset = Math.max(
      0,
      Math.min(this.selectedIndex - halfVisible, this.items.length - this.maxVisibleItems),
    )
  }
}
