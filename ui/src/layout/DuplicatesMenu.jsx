import React, { forwardRef } from 'react'
import { MenuItemLink, useTranslate } from 'react-admin'
import { MdContentCopy } from 'react-icons/md'
import { makeStyles } from '@material-ui/core'

const useStyles = makeStyles((theme) => ({
  menuItem: {
    color: theme.palette.text.secondary,
  },
}))

// DuplicatesMenu 与「同步」「在线搜索」并列的「重复歌曲」入口
// （仅管理员展示，由调用方控制渲染条件）。
const DuplicatesMenu = forwardRef(({ onClick, sidebarIsOpen, dense }, ref) => {
  const translate = useTranslate()
  const classes = useStyles()
  return (
    <MenuItemLink
      ref={ref}
      to="/duplicates"
      primaryText={translate('menu.duplicates.name')}
      leftIcon={<MdContentCopy size={24} />}
      onClick={onClick}
      className={classes.menuItem}
      sidebarIsOpen={sidebarIsOpen}
      dense={dense}
    />
  )
})

DuplicatesMenu.displayName = 'DuplicatesMenu'

export default DuplicatesMenu
