import React, { forwardRef } from 'react'
import { MenuItemLink, useTranslate } from 'react-admin'
import { MdSearch } from 'react-icons/md'
import { makeStyles } from '@material-ui/core'

const useStyles = makeStyles((theme) => ({
  menuItem: {
    color: theme.palette.text.secondary,
  },
}))

// SearchMenu 与「同步」并列的「在线搜索」入口（仅管理员展示，由调用方控制渲染条件）。
const SearchMenu = forwardRef(({ onClick, sidebarIsOpen, dense }, ref) => {
  const translate = useTranslate()
  const classes = useStyles()
  return (
    <MenuItemLink
      ref={ref}
      to="/musicsearch"
      primaryText={translate('menu.musicSearch.name')}
      leftIcon={<MdSearch size={24} />}
      onClick={onClick}
      className={classes.menuItem}
      sidebarIsOpen={sidebarIsOpen}
      dense={dense}
    />
  )
})

SearchMenu.displayName = 'SearchMenu'

export default SearchMenu
